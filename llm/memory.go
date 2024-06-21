package llm

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/format"
	"github.com/ollama/ollama/gpu"
)

// This algorithm looks for a complete fit to determine if we need to unload other models
func PredictServerFit(allGpus gpu.GpuInfoList, ggml *GGML, adapters, projectors []string, opts api.Options) (bool, uint64) {
	// Split up the GPUs by type and try them
	var estimatedVRAM uint64
	for _, gpus := range allGpus.ByLibrary() {
		var layerCount int
		estimate := EstimateGPULayers(gpus, ggml, projectors, opts)
		layerCount, estimatedVRAM = estimate.Layers, estimate.VRAMSize
		if opts.NumGPU < 0 {
			if layerCount > 0 && layerCount >= int(ggml.KV().BlockCount()+1) {
				return true, estimatedVRAM
			}
		} else {
			if layerCount > 0 && layerCount >= opts.NumGPU {
				return true, estimatedVRAM
			}
		}
	}
	return false, estimatedVRAM
}

type MemoryEstimate struct {
	// How many layers we predict we can load
	Layers int

	// The size of the graph which occupies the main GPU
	Graph uint64

	// How much VRAM will be allocated given the number of layers we predict
	VRAMSize uint64

	// The total size of the model if loaded into VRAM.  If all layers are loaded, VRAMSize == TotalSize
	TotalSize uint64

	// For multi-GPU scenarios, this provides the tensor split parameter
	TensorSplit string

	// For multi-GPU scenarios, this is the size in bytes per GPU
	GPUSizes []uint64

	// internal fields for logging purposes
	inferenceLibrary    string
	layersRequested     int
	layersModel         int
	availableList       []string
	kv                  uint64
	allocationsList     []string
	memoryWeights       uint64
	memoryLayerOutput   uint64
	graphFullOffload    uint64
	graphPartialOffload uint64
}

// Given a model and one or more GPU targets, predict how many layers and bytes we can load, and the total size
// The GPUs provided must all be the same Library
func EstimateGPULayers(gpus []gpu.GpuInfo, ggml *GGML, projectors []string, opts api.Options) MemoryEstimate {
	// Graph size for a partial offload, applies to all GPUs
	var graphPartialOffload uint64

	// Graph size when all layers are offloaded, applies to all GPUs
	var graphFullOffload uint64

	// Final graph offload once we know full or partial
	var graphOffload uint64

	// Projectors loaded into GPU0 only
	var projectorSize uint64

	// Conditional output size on GPU 0
	var memoryLayerOutput uint64

	// The sizes of a layer
	var layerSize uint64

	// The sum of all the layer sizes (just for logging)
	var memoryWeights uint64

	// True if all the layers are loaded
	var fullyLoaded bool

	// Overflow that didn't fit into the GPU
	var overflow uint64

	availableList := make([]string, len(gpus))
	for i, gpu := range gpus {
		availableList[i] = format.HumanBytes2(gpu.FreeMemory)
	}
	slog.Debug("evaluating", "library", gpus[0].Library, "gpu_count", len(gpus), "available", availableList)

	for _, projector := range projectors {
		projectorSize += projectorMemoryRequirements(projector)

		// multimodal models require at least 2048 context
		opts.NumCtx = max(opts.NumCtx, 2048)
	}

	layers := ggml.Tensors().Layers()
	// add one layer worth of memory as a buffer
	if blk0, ok := layers["blk.0"]; ok {
		layerSize = blk0.size()
	} else {
		slog.Warn("model missing blk.0 layer size")
	}

	// fp16 k,v = sizeof(float16) * n_ctx * n_layer * (n_embd_head_k + n_embd_head_v) * n_head_kv
	var kv uint64 = 2 * uint64(opts.NumCtx) * ggml.KV().BlockCount() * (ggml.KV().EmbeddingHeadCountK() + ggml.KV().EmbeddingHeadCountV()) * ggml.KV().HeadCountKV()

	// KV is proportional to the number of layers
	layerSize += kv / ggml.KV().BlockCount()

	graphPartialOffload, graphFullOffload = ggml.GraphSize(uint64(opts.NumCtx), uint64(min(opts.NumCtx, opts.NumBatch)))
	if graphPartialOffload == 0 {
		graphPartialOffload = ggml.KV().GQA() * kv / 6
	}
	if graphFullOffload == 0 {
		graphFullOffload = graphPartialOffload
	}

	// on metal there's no partial offload overhead
	if gpus[0].Library == "metal" {
		graphPartialOffload = graphFullOffload
	} else if len(gpus) > 1 {
		// multigpu should always use the partial graph size
		graphFullOffload = graphPartialOffload
	}

	if layer, ok := layers["output_norm"]; ok {
		memoryLayerOutput += layer.size()
	}
	if layer, ok := layers["output"]; ok {
		memoryLayerOutput += layer.size()
	} else if layer, ok := layers["token_embd"]; ok {
		memoryLayerOutput += layer.size()
	}

	// Output layer handled at the end if we have space
	gpuZeroOverhead := projectorSize

	// Reduce set of GPUs to only those that have sufficient space to fit overhead and at least one layer
	var layerCount int
	layerCounts := make([]int, len(gpus))
	gpuAllocations := make([]uint64, len(gpus))
	type gs struct {
		i int
		g *gpu.GpuInfo
	}
	gpusWithSpace := []gs{}
	for i := range gpus {
		var gzo uint64
		if len(gpusWithSpace) == 0 {
			gzo = gpuZeroOverhead
		}
		// Only include GPUs that can fit the graph, gpu minimum, the layer buffer and at least more layer
		if gpus[i].FreeMemory < gzo+max(graphPartialOffload, graphFullOffload)+gpus[i].MinimumMemory+2*layerSize {
			slog.Debug("gpu has too little memory to allocate any layers", "gpu", gpus[i])
			continue
		}
		gpusWithSpace = append(gpusWithSpace, gs{i, &gpus[i]})
		gpuAllocations[i] += gpus[i].MinimumMemory + layerSize // We hold off on graph until we know partial vs. full
	}

	var gpuZeroID int
	if len(gpusWithSpace) > 0 {
		gpuZeroID = gpusWithSpace[0].i
		gpuAllocations[gpuZeroID] += gpuZeroOverhead
	}

	// For all the layers, find where they can fit on the GPU(s)
	for i := range int(ggml.KV().BlockCount()) {
		// Some models have inconsistent layer sizes
		if blk, ok := layers[fmt.Sprintf("blk.%d", i)]; ok {
			layerSize = blk.size()
			layerSize += kv / ggml.KV().BlockCount()
		}
		memoryWeights += layerSize

		if opts.NumGPU >= 0 && layerCount >= opts.NumGPU {
			// Stop allocating on GPU(s) once we hit the users target NumGPU
			continue
		}

		// distribute the layers across the GPU(s) that have space
		for j := len(gpusWithSpace); j > 0; j-- {
			g := gpusWithSpace[i%j]
			used := gpuAllocations[g.i] + max(graphPartialOffload, graphFullOffload)
			if g.g.FreeMemory > used+layerSize {
				gpuAllocations[g.i] += layerSize
				layerCounts[g.i]++
				layerCount++
				break
			} else {
				gpusWithSpace = append(gpusWithSpace[:i%j], gpusWithSpace[i%j+1:]...)
			}
		}
	}
	if layerCount >= int(ggml.KV().BlockCount()) {
		fullyLoaded = true
	} else {
		for i := layerCount; i < int(ggml.KV().BlockCount()); i++ {
			overflow += layerSize
		}
	}

	// Determine if we need to consider output then find where it fits
	if memoryLayerOutput > 0 && (opts.NumGPU < 0 || layerCount < opts.NumGPU) {
		for j := len(gpusWithSpace); j > 0; j-- {
			g := gpusWithSpace[layerCount%j]
			used := gpuAllocations[g.i] + max(graphPartialOffload, graphFullOffload)
			if g.g.FreeMemory > used+memoryLayerOutput {
				gpuAllocations[g.i] += memoryLayerOutput
				layerCounts[g.i]++
				layerCount++
				break
			}
		}

		if layerCount < int(ggml.KV().BlockCount())+1 {
			fullyLoaded = false
			overflow += memoryLayerOutput
		}
	}

	// Add the applicable (full or partial) graph allocations
	for i := range gpus {
		if layerCounts[i] <= 0 {
			continue
		}
		if fullyLoaded {
			gpuAllocations[i] += graphFullOffload
		} else {
			gpuAllocations[i] += graphPartialOffload
		}
	}
	if fullyLoaded {
		graphOffload = graphFullOffload
	} else {
		graphOffload = graphPartialOffload
	}

	// Summaries for the log
	var memoryRequiredPartial, memoryRequiredTotal uint64
	for i := range gpuAllocations {
		memoryRequiredPartial += gpuAllocations[i]
	}
	memoryRequiredTotal = memoryRequiredPartial + overflow

	tensorSplit := ""
	if len(gpus) > 1 {
		splits := make([]string, len(gpus))
		for i, count := range layerCounts {
			splits[i] = strconv.Itoa(count)
		}
		tensorSplit = strings.Join(splits, ",")
	}
	allocationsList := []string{}
	for _, a := range gpuAllocations {
		allocationsList = append(allocationsList, format.HumanBytes2(a))
	}

	estimate := MemoryEstimate{
		TotalSize: memoryRequiredTotal,
		Layers:    0,
		Graph:     0,
		VRAMSize:  0,
		GPUSizes:  []uint64{},

		inferenceLibrary:    gpus[0].Library,
		layersRequested:     opts.NumGPU,
		layersModel:         int(ggml.KV().BlockCount()) + 1,
		availableList:       availableList,
		kv:                  kv,
		allocationsList:     allocationsList,
		memoryWeights:       memoryWeights,
		memoryLayerOutput:   memoryLayerOutput,
		graphFullOffload:    graphFullOffload,
		graphPartialOffload: graphPartialOffload,
	}

	if gpus[0].Library == "cpu" {
		return estimate
	}
	if layerCount == 0 {
		slog.Debug("insufficient VRAM to load any model layers")
		return estimate
	}
	estimate.Layers = layerCount
	estimate.Graph = graphOffload
	estimate.VRAMSize = memoryRequiredPartial
	estimate.TotalSize = memoryRequiredTotal
	estimate.TensorSplit = tensorSplit
	estimate.GPUSizes = gpuAllocations
	return estimate
}

func (m MemoryEstimate) log() {
	slog.Info(
		"offload to "+m.inferenceLibrary,
		slog.Group(
			"layers",
			// requested number of layers to offload
			"requested", m.layersRequested,
			// The number of layers the model has (including output)
			"model", m.layersModel,
			// estimated number of layers that can be offloaded
			"offload", m.Layers,
			// multi-gpu split for tensors
			"split", m.TensorSplit,
		),
		slog.Group(
			"memory",
			// memory available by GPU for offloading
			"available", m.availableList,
			slog.Group(
				"required",
				// memory required for full offloading
				"full", format.HumanBytes2(m.TotalSize),
				// memory required to offload layers.estimate layers
				"partial", format.HumanBytes2(m.VRAMSize),
				// memory of KV cache
				"kv", format.HumanBytes2(m.kv),
				// Allocations across the GPUs
				"allocations", m.allocationsList,
			),
			slog.Group(
				"weights",
				// memory of the weights
				"total", format.HumanBytes2(m.memoryWeights),
				// memory of repeating layers
				"repeating", format.HumanBytes2(m.memoryWeights-m.memoryLayerOutput),
				// memory of non-repeating layers
				"nonrepeating", format.HumanBytes2(m.memoryLayerOutput),
			),
			slog.Group(
				"graph",
				// memory of graph when fully offloaded
				"full", format.HumanBytes2(m.graphFullOffload),
				// memory of graph when not fully offloaded
				"partial", format.HumanBytes2(m.graphPartialOffload),
			),
		),
	)
}
