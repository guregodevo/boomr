package vast

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a vast.ai API in memory: it records each request and answers from
// handlers keyed by "METHOD path".
type fake struct {
	mu       sync.Mutex
	requests []recorded
	handlers map[string]func(w http.ResponseWriter, body map[string]any)
}

type recorded struct {
	method, path, query, auth string
	body                      map[string]any
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
	h := f.handlers[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if h == nil {
		http.Error(w, `{"error":"no_such_instance"}`, http.StatusNotFound)
		return
	}
	h(w, body)
}

func (f *fake) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func newFake(t *testing.T, key string) (*fake, *Client) {
	f := &fake{handlers: map[string]func(http.ResponseWriter, map[string]any){}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &Client{BaseURL: srv.URL, Key: key, HTTP: srv.Client(), Poll: time.Millisecond}
}

func reply(v any) func(http.ResponseWriter, map[string]any) {
	return func(w http.ResponseWriter, _ map[string]any) { json.NewEncoder(w).Encode(v) }
}

func TestOffersQueryAndFilter(t *testing.T) {
	f, c := newFake(t, "")
	f.handlers["GET /bundles/"] = reply(map[string]any{"offers": []map[string]any{
		{"id": 1, "machine_id": 10, "gpu_name": "H100 SXM", "dph_total": 1.5, "verification": "deverified", "geolocation": "Japan, JP"},
		{"id": 2, "machine_id": 20, "gpu_name": "H100 SXM", "dph_total": 2.0, "verification": "verified", "geolocation": "Montana, US"},
		{"id": 3, "machine_id": 30, "gpu_name": "H100 SXM", "dph_total": 2.1, "verification": "verified", "geolocation": "Hong Kong, HK"},
		{"id": 4, "machine_id": 40, "gpu_name": "H100 SXM", "dph_total": 2.2, "verification": "verified", "geolocation": "Germany, DE"},
	}})
	os, err := c.Offers(context.Background(), Selectors{GPUNames: []string{"H100 SXM"}, MinGPURAMGB: 80, MaxPrice: 3,
		ExcludeMachines: map[int64]bool{40: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(os) != 1 || os[0].ID != 2 {
		t.Fatalf("kept %+v, want offer 2 alone (1 deverified, 3 in HK, 4 excluded)", os)
	}
	r := f.last()
	if r.auth != "" {
		t.Error("the search sent a key")
	}
	qs, _ := url.ParseQuery(r.query)
	var q map[string]any
	if err := json.Unmarshal([]byte(qs.Get("q")), &q); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"verified":          `{"eq":true}`,
		"gpu_name":          `{"in":["H100 SXM"]}`,
		"gpu_ram":           `{"gte":81920}`,
		"dph_total":         `{"lte":3}`,
		"num_gpus":          `{"eq":1}`,
		"disk_space":        `{"gte":60}`,
		"inet_down":         `{"gte":500}`,
		"cuda_max_good":     `{"gte":12.8}`,
		"direct_port_count": `{"gte":2}`,
		"geolocation":       `{"notin":["CN","HK"]}`,
		"type":              `"on-demand"`,
	}
	for k, v := range want {
		got, _ := json.Marshal(q[k])
		if string(got) != v {
			t.Errorf("query %s = %s, want %s", k, got, v)
		}
	}

	if _, err := c.Offers(context.Background(), Selectors{AllowUnverified: true}); err != nil {
		t.Fatal(err)
	}
	qs, _ = url.ParseQuery(f.last().query)
	if strings.Contains(qs.Get("q"), `"verified"`) {
		t.Error("an unverified search still asks for verified hosts")
	}
	if _, err := c.Offers(context.Background(), Selectors{RentalType: "spot"}); err == nil {
		t.Error("a rental type that is neither on-demand nor bid was searched")
	}
}

func TestCreate(t *testing.T) {
	f, c := newFake(t, "k")
	f.handlers["PUT /asks/7/"] = reply(map[string]any{"success": true, "new_contract": 99})
	id, label, err := c.Create(context.Background(), 7, Spec{Image: "nvidia/cuda:12.9.1-devel-ubuntu22.04", DiskGB: 150, OnStart: "echo hi"})
	if err != nil || id != 99 {
		t.Fatalf("Create = %d, %v", id, err)
	}
	r := f.last()
	if r.auth != "Bearer k" {
		t.Errorf("auth %q", r.auth)
	}
	if r.body["image"] != "nvidia/cuda:12.9.1-devel-ubuntu22.04" || r.body["disk"] != 150.0 ||
		r.body["runtype"] != "ssh_direc ssh_proxy" || r.body["onstart"] != "echo hi" ||
		r.body["label"] != label || !strings.HasPrefix(label, "boomr-") || r.body["client_id"] != "me" {
		t.Errorf("create body %v", r.body)
	}
	if _, ok := r.body["price"]; ok {
		t.Error("an on-demand rental sent a bid price")
	}
}

// The create reply is wrong on bid rentals: the instance is found by label.
func TestCreateFindsTheInstanceByLabel(t *testing.T) {
	f, c := newFake(t, "k")
	f.handlers["PUT /asks/7/"] = func(w http.ResponseWriter, _ map[string]any) {
		http.Error(w, `{"msg":"timeout"}`, http.StatusInternalServerError)
	}
	f.handlers["GET /instances/"] = reply(map[string]any{"instances": []map[string]any{
		{"id": 5, "label": "other"}, {"id": 42, "label": "mine"},
	}})
	id, _, err := c.Create(context.Background(), 7, Spec{Image: "img:1", DiskGB: 10, Label: "mine", BidPrice: 0.9})
	if err != nil || id != 42 {
		t.Fatalf("Create = %d, %v; want 42 found by its label", id, err)
	}
	for _, r := range f.requests {
		if r.method == "PUT" && r.body["price"] != 0.9 {
			t.Errorf("bid price %v", r.body["price"])
		}
	}
}

func TestSpecRefusesWhatCannotBeReproduced(t *testing.T) {
	for _, s := range []Spec{
		{Image: "", DiskGB: 10},
		{Image: "nvidia/cuda", DiskGB: 10},
		{Image: "vllm/vllm-openai:latest", DiskGB: 10},
		{Image: "img:1", DiskGB: 0},
		{Image: "img:1", DiskGB: 10, RunType: RunArgs},
		{Image: "img:1", DiskGB: 10, RunType: "jupyter"},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("spec %+v was taken", s)
		}
	}
	for _, s := range []Spec{
		{Image: "localhost:5000/img:1", DiskGB: 10},
		{Image: "img@sha256:abc", DiskGB: 10},
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("spec %+v: %v", s, err)
		}
	}
}

func TestInstanceAndGone(t *testing.T) {
	f, c := newFake(t, "k")
	f.handlers["GET /instances/42/"] = reply(map[string]any{"instances": map[string]any{
		"id": 42, "actual_status": "running", "public_ipaddr": "1.2.3.4", "ssh_host": "ssh5.vast.ai", "ssh_port": 12345,
		"ports": map[string]any{"22/tcp": []map[string]string{{"HostPort": "40022"}}, "8000/tcp": []map[string]string{{"HostPort": "40800"}}},
	}})
	in, err := c.Instance(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if host, port, ok := in.SSH(); !ok || host != "1.2.3.4" || port != "40022" {
		t.Errorf("SSH = %s %s %v, want the direct port", host, port, ok)
	}
	if e := in.Endpoint("8000/tcp"); e != "http://1.2.3.4:40800" {
		t.Errorf("Endpoint = %q", e)
	}
	in.Ports = nil
	if host, port, _ := in.SSH(); host != "ssh5.vast.ai" || port != "12345" {
		t.Errorf("SSH without a direct port = %s %s, want the proxy", host, port)
	}
	if _, err := c.Instance(context.Background(), 7); !errors.Is(err, ErrInstanceGone) {
		t.Errorf("a 404 instance: %v, want ErrInstanceGone", err)
	}
}

func TestStateAndKey(t *testing.T) {
	f, c := newFake(t, "k")
	f.handlers["PUT /instances/42/"] = reply(map[string]any{"success": true})
	if err := c.Stop(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if f.last().body["state"] != "stopped" {
		t.Errorf("stop sent %v", f.last().body)
	}
	if err := c.Start(context.Background(), 42); err != nil || f.last().body["state"] != "running" {
		t.Errorf("start: %v, %v", err, f.last().body)
	}
	c.Key = ""
	if err := c.Destroy(context.Background(), 42); !errors.Is(err, ErrNoKey) {
		t.Errorf("destroy with no key: %v", err)
	}
}
