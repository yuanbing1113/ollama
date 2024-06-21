package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/llm"
)

var stream bool = false

func createBinFile(t *testing.T, kv map[string]any, ti []llm.Tensor) string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := llm.NewGGUFV3(binary.LittleEndian).Encode(f, kv, ti); err != nil {
		t.Fatal(err)
	}

	return f.Name()
}

type responseRecorder struct {
	*httptest.ResponseRecorder
	http.CloseNotifier
}

func NewRecorder() *responseRecorder {
	return &responseRecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}
}

func (t *responseRecorder) CloseNotify() <-chan bool {
	return make(chan bool)
}

func createRequest(t *testing.T, fn func(*gin.Context), body any) *httptest.ResponseRecorder {
	t.Helper()

	w := NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(body); err != nil {
		t.Fatal(err)
	}

	c.Request = &http.Request{
		Body: io.NopCloser(&b),
	}

	fn(c)
	return w.ResponseRecorder
}

func checkFileExists(t *testing.T, p string, expect []string) {
	t.Helper()

	actual, err := filepath.Glob(p)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(actual, expect) {
		t.Fatalf("expected slices to be equal %v", actual)
	}
}

func TestCreateFromBin(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()

	var s Server
	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-ca239d7bd8ea90e4a5d2e6bf88f8d74a47b14336e73eb4e18bed4dd325018116"),
	})
}

func TestCreateFromModel(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test2",
		Modelfile: "FROM test",
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test2", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-ca239d7bd8ea90e4a5d2e6bf88f8d74a47b14336e73eb4e18bed4dd325018116"),
	})
}

func TestCreateRemovesLayers(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nTEMPLATE {{ .Prompt }}", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-b507b9c2f6ca642bffcd06665ea7c91f235fd32daeefdf875a0f938db05fb315"),
		filepath.Join(p, "blobs", "sha256-bc80b03733773e0728011b2f4adf34c458b400e1aad48cb28d61170f3a2ad2d6"),
	})

	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nTEMPLATE {{ .System }} {{ .Prompt }}", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-8f2c2167d789c6b2302dff965160fa5029f6a24096d262c1cbb469f21a045382"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-fe7ac77b725cda2ccad03f88a880ecdfd7a33192d6cae08fce2c0ee1455991ed"),
	})
}

func TestCreateUnsetsSystem(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nSYSTEM Say hi!", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-8585df945d1069bc78b79bd10bb73ba07fbc29b0f5479a31a601c0d12731416e"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-f29e82a8284dbdf5910b1555580ff60b04238b8da9d5e51159ada67a4d0d5851"),
	})

	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nSYSTEM \"\"", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-67d4b8d106af2a5b100a46e9bdc038c71eef2a35c9abac784092654212f97cf5"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
	})

	bts, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"))
	if err != nil {
		t.Fatal(err)
	}

	if string(bts) != "" {
		t.Fatalf("expected empty string, actual %s", string(bts))
	}
}

func TestCreateMergeParameters(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nPARAMETER temperature 1\nPARAMETER top_k 10\nPARAMETER stop USER:\nPARAMETER stop ASSISTANT:", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-1d0ad71299d48c2fb7ae2b98e683643e771f8a5b72be34942af90d97a91c1e37"),
		filepath.Join(p, "blobs", "sha256-4a384beaf47a9cbe452dfa5ab70eea691790f3b35a832d12933a1996685bf2b6"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
	})

	// in order to merge parameters, the second model must be created FROM the first
	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test2",
		Modelfile: "FROM test\nPARAMETER temperature 0.6\nPARAMETER top_p 0.7",
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test2", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-1d0ad71299d48c2fb7ae2b98e683643e771f8a5b72be34942af90d97a91c1e37"),
		filepath.Join(p, "blobs", "sha256-4a384beaf47a9cbe452dfa5ab70eea691790f3b35a832d12933a1996685bf2b6"),
		filepath.Join(p, "blobs", "sha256-4cd9d4ba6b734d9b4cbd1e5caa60374c00722e993fce5e1e2d15a33698f71187"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-e29a7b3c47287a2489c895d21fe413c20f859a85d20e749492f52a838e36e1ba"),
	})

	actual, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-e29a7b3c47287a2489c895d21fe413c20f859a85d20e749492f52a838e36e1ba"))
	if err != nil {
		t.Fatal(err)
	}

	expect, err := json.Marshal(map[string]any{"temperature": 0.6, "top_k": 10, "top_p": 0.7, "stop": []string{"USER:", "ASSISTANT:"}})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytes.TrimSpace(expect), bytes.TrimSpace(actual)) {
		t.Errorf("expected %s, actual %s", string(expect), string(actual))
	}

	// slices are replaced
	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test2",
		Modelfile: "FROM test\nPARAMETER temperature 0.6\nPARAMETER top_p 0.7\nPARAMETER stop <|endoftext|>",
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test2", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-12f58bb75cb3042d69a7e013ab87fb3c3c7088f50ddc62f0c77bd332f0d44d35"),
		filepath.Join(p, "blobs", "sha256-1d0ad71299d48c2fb7ae2b98e683643e771f8a5b72be34942af90d97a91c1e37"),
		filepath.Join(p, "blobs", "sha256-257aa726584f24970a4f240765e75a7169bfbe7f4966c1f04513d6b6c860583a"),
		filepath.Join(p, "blobs", "sha256-4a384beaf47a9cbe452dfa5ab70eea691790f3b35a832d12933a1996685bf2b6"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
	})

	actual, err = os.ReadFile(filepath.Join(p, "blobs", "sha256-12f58bb75cb3042d69a7e013ab87fb3c3c7088f50ddc62f0c77bd332f0d44d35"))
	if err != nil {
		t.Fatal(err)
	}

	expect, err = json.Marshal(map[string]any{"temperature": 0.6, "top_k": 10, "top_p": 0.7, "stop": []string{"<|endoftext|>"}})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bytes.TrimSpace(expect), bytes.TrimSpace(actual)) {
		t.Errorf("expected %s, actual %s", string(expect), string(actual))
	}
}

func TestCreateReplacesMessages(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nMESSAGE assistant \"What is my purpose?\"\nMESSAGE user \"You run tests.\"\nMESSAGE assistant \"Oh, my god.\"", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-298baeaf6928a60cf666d88d64a1ba606feb43a2865687c39e40652e407bffc4"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-e0e27d47045063ccb167ae852c51d49a98eab33fabaee4633fdddf97213e40b5"),
	})

	w = createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test2",
		Modelfile: "FROM test\nMESSAGE assistant \"You're a test, Harry.\"\nMESSAGE user \"I-I'm a what?\"\nMESSAGE assistant \"A test. And a thumping good one at that, I'd wager.\"",
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test2", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-298baeaf6928a60cf666d88d64a1ba606feb43a2865687c39e40652e407bffc4"),
		filepath.Join(p, "blobs", "sha256-4f48b25fe9969564c82f58eb1cedbdff6484cc0baf474bc6c2a9b37c8da3362a"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-a60ecc9da299ec7ede453f99236e5577fd125e143689b646d9f0ddc9971bf4db"),
		filepath.Join(p, "blobs", "sha256-e0e27d47045063ccb167ae852c51d49a98eab33fabaee4633fdddf97213e40b5"),
	})

	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	f, err := os.Open(filepath.Join(p, "blobs", "sha256-a60ecc9da299ec7ede453f99236e5577fd125e143689b646d9f0ddc9971bf4db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var actual []message
	if err := json.NewDecoder(f).Decode(&actual); err != nil {
		t.Fatal(err)
	}

	expect := []message{
		{Role: "assistant", Content: "You're a test, Harry."},
		{Role: "user", Content: "I-I'm a what?"},
		{Role: "assistant", Content: "A test. And a thumping good one at that, I'd wager."},
	}

	if !slices.Equal(actual, expect) {
		t.Errorf("expected %s, actual %s", expect, actual)
	}
}

func TestCreateTemplateSystem(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nTEMPLATE {{ .Prompt }}\nSYSTEM Say hello!\nTEMPLATE {{ .System }} {{ .Prompt }}\nSYSTEM Say bye!", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-2b5e330885117c82f3fd75169ea323e141070a2947c11ddb9f79ee0b01c589c1"),
		filepath.Join(p, "blobs", "sha256-4c5f51faac758fecaff8db42f0b7382891a4d0c0bb885f7b86be88c814a7cc86"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-fe7ac77b725cda2ccad03f88a880ecdfd7a33192d6cae08fce2c0ee1455991ed"),
	})

	template, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-fe7ac77b725cda2ccad03f88a880ecdfd7a33192d6cae08fce2c0ee1455991ed"))
	if err != nil {
		t.Fatal(err)
	}

	if string(template) != "{{ .System }} {{ .Prompt }}" {
		t.Errorf("expected \"{{ .System }} {{ .Prompt }}\", actual %s", template)
	}

	system, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-4c5f51faac758fecaff8db42f0b7382891a4d0c0bb885f7b86be88c814a7cc86"))
	if err != nil {
		t.Fatal(err)
	}

	if string(system) != "Say bye!" {
		t.Errorf("expected \"Say bye!\", actual %s", system)
	}
}

func TestCreateLicenses(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
		Name:      "test",
		Modelfile: fmt.Sprintf("FROM %s\nLICENSE MIT\nLICENSE Apache-2.0", createBinFile(t, nil, nil)),
		Stream:    &stream,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status code 200, actual %d", w.Code)
	}

	checkFileExists(t, filepath.Join(p, "manifests", "*", "*", "*", "*"), []string{
		filepath.Join(p, "manifests", "registry.ollama.ai", "library", "test", "latest"),
	})

	checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
		filepath.Join(p, "blobs", "sha256-2af71558e438db0b73a20beab92dc278a94e1bbe974c00c1a33e3ab62d53a608"),
		filepath.Join(p, "blobs", "sha256-79a39c37536ddee29cbadd5d5e2dcba8ed7f03e431f626ff38432c1c866bb7e2"),
		filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
		filepath.Join(p, "blobs", "sha256-e5dcffe836b6ec8a58e492419b550e65fb8cbdc308503979e5dacb33ac7ea3b7"),
	})

	mit, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-e5dcffe836b6ec8a58e492419b550e65fb8cbdc308503979e5dacb33ac7ea3b7"))
	if err != nil {
		t.Fatal(err)
	}

	if string(mit) != "MIT" {
		t.Errorf("expected MIT, actual %s", mit)
	}

	apache, err := os.ReadFile(filepath.Join(p, "blobs", "sha256-2af71558e438db0b73a20beab92dc278a94e1bbe974c00c1a33e3ab62d53a608"))
	if err != nil {
		t.Fatal(err)
	}

	if string(apache) != "Apache-2.0" {
		t.Errorf("expected Apache-2.0, actual %s", apache)
	}
}

func TestCreateDetectTemplate(t *testing.T) {
	p := t.TempDir()
	t.Setenv("OLLAMA_MODELS", p)
	envconfig.LoadConfig()
	var s Server

	t.Run("matched", func(t *testing.T) {
		w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
			Name: "test",
			Modelfile: fmt.Sprintf("FROM %s", createBinFile(t, llm.KV{
				"tokenizer.chat_template": "{{ bos_token }}{% for message in messages %}{{'<|' + message['role'] + '|>' + '\n' + message['content'] + '<|end|>\n' }}{% endfor %}{% if add_generation_prompt %}{{ '<|assistant|>\n' }}{% else %}{{ eos_token }}{% endif %}",
			}, nil)),
			Stream: &stream,
		})

		if w.Code != http.StatusOK {
			t.Fatalf("expected status code 200, actual %d", w.Code)
		}

		checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
			filepath.Join(p, "blobs", "sha256-2f8e594e6f34b1b4d36a246628eeb3365ce442303d656f1fcc69e821722acea0"),
			filepath.Join(p, "blobs", "sha256-542b217f179c7825eeb5bca3c77d2b75ed05bafbd3451d9188891a60a85337c6"),
			filepath.Join(p, "blobs", "sha256-553c4a3f747b3d22a4946875f1cc8ed011c2930d83f864a0c7265f9ec0a20413"),
		})
	})

	t.Run("unmatched", func(t *testing.T) {
		w := createRequest(t, s.CreateModelHandler, api.CreateRequest{
			Name:      "test",
			Modelfile: fmt.Sprintf("FROM %s", createBinFile(t, nil, nil)),
			Stream:    &stream,
		})

		if w.Code != http.StatusOK {
			t.Fatalf("expected status code 200, actual %d", w.Code)
		}

		checkFileExists(t, filepath.Join(p, "blobs", "*"), []string{
			filepath.Join(p, "blobs", "sha256-a4e5e156ddec27e286f75328784d7106b60a4eb1d246e950a001a3f944fbda99"),
			filepath.Join(p, "blobs", "sha256-ca239d7bd8ea90e4a5d2e6bf88f8d74a47b14336e73eb4e18bed4dd325018116"),
		})
	})
}
