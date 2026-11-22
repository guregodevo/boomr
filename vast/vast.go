// Package vast is a client for vast.ai's REST API (v0): search GPU offers,
// rent one, read it, stop, start and destroy it. Ported from memdoor's
// pkg/provision (removed 2026-09-18); the rules it learned on live rentals
// are kept with their dates.
package vast

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is vast.ai's API root.
const DefaultBaseURL = "https://console.vast.ai/api/v0"

// KeyEnv names the environment variable that holds the API key.
const KeyEnv = "VAST_API_KEY"

// ErrNoKey is returned by a call that needs the API key when none is set.
var ErrNoKey = errors.New("no vast.ai API key: set " + KeyEnv + " or run `vastai set api-key`")

// ErrInstanceGone means vast.ai no longer has the instance: it was destroyed
// outside this client and no call on it will succeed again. A machine that is
// not there is not an outage (2026-09-17: a machine deleted by hand answered
// no_such_instance to everything, and was taken for a failure to retry).
var ErrInstanceGone = errors.New("instance no longer exists at vast.ai")

// ErrUnavailable means vast.ai refused a start because the host's GPUs are
// rented to someone else. It answers 200 with "success": false and queues
// the change: the instance starts, and bills, when the GPUs free up, unless
// a stop is sent first.
var ErrUnavailable = errors.New("the host's GPUs are rented to someone else")

// Client talks to one vast.ai account. Key is empty for the public calls only.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	// Poll is the wait between the reads that follow a create or a log
	// request.
	Poll time.Duration
}

// New returns a client with the key from the environment or the vastai CLI.
func New() *Client {
	return &Client{BaseURL: DefaultBaseURL, Key: LoadKey(), HTTP: &http.Client{Timeout: 60 * time.Second}, Poll: 3 * time.Second}
}

// LoadKey reads VAST_API_KEY, else the file the vastai CLI writes
// (~/.config/vastai/vast_api_key, or ~/.vast_api_key from older versions).
func LoadKey() string {
	if k := strings.TrimSpace(os.Getenv(KeyEnv)); k != "" {
		return k
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, p := range []string{
		filepath.Join(home, ".config", "vastai", "vast_api_key"),
		filepath.Join(home, ".vast_api_key"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			if k := strings.TrimSpace(string(b)); k != "" {
				return k
			}
		}
	}
	return ""
}

// Offer is one rentable machine, with the hardware vast.ai measures on it.
type Offer struct {
	ID int64 `json:"id"`
	// MachineID is the physical host, which several offers share: a host that
	// cannot run the image is bad for every offer on it.
	MachineID    int64   `json:"machine_id"`
	HostID       int64   `json:"host_id"`
	GPUName      string  `json:"gpu_name"`
	NumGPUs      int     `json:"num_gpus"`
	GPURAMMB     float64 `json:"gpu_ram"`
	GPUMemBW     float64 `json:"gpu_mem_bw"` // GB/s: what decode speed follows
	TotalFlops   float64 `json:"total_flops"`
	ComputeCap   int     `json:"compute_cap"` // x100, 900 = 9.0
	PCIeBW       float64 `json:"pcie_bw"`
	CUDAMax      float64 `json:"cuda_max_good"`
	DPHTotal     float64 `json:"dph_total"`
	MinBid       float64 `json:"min_bid"`
	Verification string  `json:"verification"`
	Reliability  float64 `json:"reliability2"`
	Geolocation  string  `json:"geolocation"`
	DiskSpace    float64 `json:"disk_space"`
	DiskBW       float64 `json:"disk_bw"`   // MB/s
	InetDown     float64 `json:"inet_down"` // Mb/s
}

// GPURAMGB is one GPU's memory in GB.
func (o Offer) GPURAMGB() float64 { return o.GPURAMMB / 1024 }

// Verified reports whether vast.ai verifies the host now.
func (o Offer) Verified() bool { return o.Verification == "verified" }

// Rental types.
const (
	OnDemand = "on-demand"
	Bid      = "bid"
)

// Selectors is what a host must satisfy. Zero fields take DefaultSelectors'
// value, so a caller sets only what it cares about.
type Selectors struct {
	GPUNames    []string
	NumGPUs     int
	MinGPURAMGB float64
	// MaxPrice caps the whole machine's $/h. 0 is no cap.
	MaxPrice float64
	// RentalType is OnDemand or Bid (interruptible).
	RentalType string
	// MinDiskGB must hold the image and everything the machine downloads:
	// too small and the container dies mid-download, billed like a success.
	MinDiskGB float64
	// MinDownMbps decides how long a fresh machine is paid for while it
	// downloads rather than works.
	MinDownMbps float64
	// MinCUDA is the host driver's newest CUDA. A driver older than the
	// image's runtime fails at init (CUDA error 804, 2026-08-21). Necessary,
	// not sufficient.
	MinCUDA float64
	// MinDirectPorts: a machine reached over the network needs a port.
	MinDirectPorts int
	// ExcludeGeos are country codes to avoid.
	ExcludeGeos []string
	// AllowUnverified takes hosts vast.ai does not verify: cheaper, and
	// likelier to vanish mid-session. Off by default.
	AllowUnverified bool
	// ExcludeMachines are hosts already known bad: a retry skips the machine,
	// not only the offer that failed.
	ExcludeMachines map[int64]bool
	Limit           int
}

// DefaultSelectors is what pkg/provision required when nothing else was said.
func DefaultSelectors() Selectors {
	return Selectors{
		NumGPUs:        1,
		RentalType:     OnDemand,
		MinDiskGB:      60,
		MinDownMbps:    500,
		MinCUDA:        12.8,
		MinDirectPorts: 2,
		ExcludeGeos:    []string{"CN", "HK"},
		Limit:          10,
	}
}

// withDefaults fills unset fields.
func (s Selectors) withDefaults() Selectors {
	d := DefaultSelectors()
	d.GPUNames, d.MinGPURAMGB, d.MaxPrice = s.GPUNames, s.MinGPURAMGB, s.MaxPrice
	d.AllowUnverified, d.ExcludeMachines = s.AllowUnverified, s.ExcludeMachines
	if s.NumGPUs > 0 {
		d.NumGPUs = s.NumGPUs
	}
	if s.RentalType != "" {
		d.RentalType = s.RentalType
	}
	if s.MinDiskGB > 0 {
		d.MinDiskGB = s.MinDiskGB
	}
	if s.MinDownMbps > 0 {
		d.MinDownMbps = s.MinDownMbps
	}
	if s.MinCUDA > 0 {
		d.MinCUDA = s.MinCUDA
	}
	if s.MinDirectPorts > 0 {
		d.MinDirectPorts = s.MinDirectPorts
	}
	if s.ExcludeGeos != nil {
		d.ExcludeGeos = s.ExcludeGeos
	}
	if s.Limit > 0 {
		d.Limit = s.Limit
	}
	return d
}

// Validate refuses selectors that cannot match anything.
func (s Selectors) Validate() error {
	if s.RentalType != "" && s.RentalType != OnDemand && s.RentalType != Bid {
		return fmt.Errorf("vast: rental type %q is neither %s nor %s", s.RentalType, OnDemand, Bid)
	}
	if s.MaxPrice < 0 || s.MinGPURAMGB < 0 || s.MinDiskGB < 0 {
		return errors.New("vast: a price, memory or disk floor cannot be negative")
	}
	return nil
}

func (s Selectors) query() map[string]any {
	q := map[string]any{
		"rentable":          map[string]any{"eq": true},
		"rented":            map[string]any{"eq": false},
		"num_gpus":          map[string]any{"eq": s.NumGPUs},
		"type":              s.RentalType,
		"order":             [][]string{{"dph_total", "asc"}},
		"limit":             s.Limit*4 + 16,
		"disk_space":        map[string]any{"gte": s.MinDiskGB},
		"inet_down":         map[string]any{"gte": s.MinDownMbps},
		"cuda_max_good":     map[string]any{"gte": s.MinCUDA},
		"direct_port_count": map[string]any{"gte": s.MinDirectPorts},
	}
	if !s.AllowUnverified {
		q["verified"] = map[string]any{"eq": true}
	}
	if len(s.GPUNames) > 0 {
		q["gpu_name"] = map[string]any{"in": s.GPUNames}
	}
	if s.MinGPURAMGB > 0 {
		q["gpu_ram"] = map[string]any{"gte": s.MinGPURAMGB * 1024}
	}
	if s.MaxPrice > 0 {
		q["dph_total"] = map[string]any{"lte": s.MaxPrice}
	}
	if len(s.ExcludeGeos) > 0 {
		q["geolocation"] = map[string]any{"notin": s.ExcludeGeos}
	}
	return q
}

// keep is what vast.ai's search does not filter on its own: a deverified
// host it still lists as verified, a host already known bad.
func (s Selectors) keep(o Offer) bool {
	if !s.AllowUnverified && !o.Verified() {
		return false
	}
	if s.ExcludeMachines[o.MachineID] {
		return false
	}
	for _, g := range s.ExcludeGeos {
		if strings.HasSuffix(o.Geolocation, ", "+g) {
			return false
		}
	}
	return true
}

// Offers returns the matching offers, cheapest first. It needs no key.
func (c *Client) Offers(ctx context.Context, sel Selectors) ([]Offer, error) {
	if err := sel.Validate(); err != nil {
		return nil, err
	}
	sel = sel.withDefaults()
	b, err := json.Marshal(sel.query())
	if err != nil {
		return nil, err
	}
	var out struct {
		Offers []Offer `json:"offers"`
	}
	if err := c.do(ctx, http.MethodGet, "/bundles/?q="+url.QueryEscape(string(b)), nil, &out, false); err != nil {
		return nil, err
	}
	var kept []Offer
	for _, o := range out.Offers {
		if sel.keep(o) {
			kept = append(kept, o)
		}
	}
	if len(kept) > sel.Limit {
		kept = kept[:sel.Limit]
	}
	return kept, nil
}

// Run types: an SSH machine that runs OnStart, or a container that runs its
// image's entrypoint with Args.
const (
	RunSSH  = "ssh"
	RunArgs = "args"
)

// Spec is what to run on a rented machine.
type Spec struct {
	// Image is the Docker image, pinned: a moving tag (":latest", or none)
	// means a failure cannot be reproduced nor a success repeated.
	Image   string
	DiskGB  float64
	RunType string // RunSSH (default) or RunArgs
	OnStart string // RunSSH: the script run at start
	Args    []string
	Env     map[string]string
	// Label names the instance; a unique one is minted when empty. It is
	// also how the instance is found when the create reply does not name it.
	Label string
	// BidPrice > 0 rents the interruptible tier at that $/h.
	BidPrice float64
}

// Validate refuses a spec before money is spent on it.
func (s Spec) Validate() error {
	img := s.Image
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	switch {
	case s.Image == "":
		return errors.New("vast: no image")
	case !strings.Contains(img, ":") && !strings.Contains(img, "@"):
		return fmt.Errorf("vast: image %q names no tag; pin a release", s.Image)
	case strings.HasSuffix(s.Image, ":latest"):
		return fmt.Errorf("vast: image %q is not pinned to a release", s.Image)
	case s.DiskGB <= 0:
		return errors.New("vast: no disk size")
	case s.RunType != "" && s.RunType != RunSSH && s.RunType != RunArgs:
		return fmt.Errorf("vast: run type %q (ssh or args)", s.RunType)
	case s.RunType == RunArgs && len(s.Args) == 0:
		return errors.New("vast: an args instance with no args")
	}
	return nil
}

func newLabel() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "boomr-" + hex.EncodeToString(b)
}

// Create rents offer id with spec and returns the new instance's id and the
// label it carries.
func (c *Client) Create(ctx context.Context, offerID int64, s Spec) (int64, string, error) {
	if err := s.Validate(); err != nil {
		return 0, "", err
	}
	if s.Label == "" {
		s.Label = newLabel()
	}
	body := map[string]any{
		"client_id": "me",
		"image":     s.Image,
		"disk":      s.DiskGB,
		"label":     s.Label,
		"env":       s.Env,
	}
	if s.RunType == RunArgs {
		body["runtype"], body["args"], body["onstart"] = "args", s.Args, ""
	} else {
		body["runtype"], body["onstart"] = "ssh_direc ssh_proxy", s.OnStart
	}
	if s.BidPrice > 0 {
		body["price"] = s.BidPrice
	}
	var out struct {
		Success     bool   `json:"success"`
		NewContract int64  `json:"new_contract"`
		Msg         string `json:"msg"`
	}
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/asks/%d/", offerID), body, &out, true)
	if err == nil && out.NewContract != 0 {
		return out.NewContract, s.Label, nil
	}
	if errors.Is(err, ErrNoKey) {
		return 0, "", err
	}
	// The create reply is wrong both ways on bid rentals (pkg/provision):
	// an error for a machine that was made, no id for one that was. The
	// label is the truth.
	for attempt := 0; attempt < 5; attempt++ {
		if !sleep(ctx, c.Poll) {
			break
		}
		if in, ferr := c.FindByLabel(ctx, s.Label); ferr == nil {
			return in.ID, s.Label, nil
		}
	}
	if err != nil {
		return 0, "", err
	}
	return 0, "", fmt.Errorf("vast: offer %d not rented: %s", offerID, out.Msg)
}

// Instance is one machine the account rents.
type Instance struct {
	ID           int64   `json:"id"`
	MachineID    int64   `json:"machine_id"`
	Label        string  `json:"label"`
	ActualStatus string  `json:"actual_status"`
	Intended     string  `json:"intended_status"`
	StatusMsg    string  `json:"status_msg"`
	GPUName      string  `json:"gpu_name"`
	NumGPUs      int     `json:"num_gpus"`
	GPUUtil      float64 `json:"gpu_util"`
	DPHTotal     float64 `json:"dph_total"`
	StartDate    float64 `json:"start_date"`
	SSHHost      string  `json:"ssh_host"`
	SSHPort      int     `json:"ssh_port"`
	PublicIP     string  `json:"public_ipaddr"`
	Geolocation  string  `json:"geolocation"`
	Image        string  `json:"image_uuid"`
	Ports        map[string][]struct {
		HostPort string `json:"HostPort"`
	} `json:"ports"`
}

// Running reports whether the machine is up.
func (i Instance) Running() bool { return i.ActualStatus == "running" }

// SSH returns the address to ssh to: the direct port when the host maps one,
// else vast.ai's proxy.
func (i Instance) SSH() (host string, port string, ok bool) {
	if b := i.Ports["22/tcp"]; i.PublicIP != "" && len(b) > 0 && b[0].HostPort != "" {
		return i.PublicIP, b[0].HostPort, true
	}
	if i.SSHHost != "" && i.SSHPort != 0 {
		return i.SSHHost, fmt.Sprint(i.SSHPort), true
	}
	return "", "", false
}

// Endpoint is http://ip:port for a port the container exposes ("8000/tcp").
func (i Instance) Endpoint(containerPort string) string {
	if b := i.Ports[containerPort]; i.PublicIP != "" && len(b) > 0 && b[0].HostPort != "" {
		return fmt.Sprintf("http://%s:%s", i.PublicIP, b[0].HostPort)
	}
	return ""
}

// Instances lists the account's instances.
func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var out struct {
		Instances []Instance `json:"instances"`
	}
	if err := c.do(ctx, http.MethodGet, "/instances/?owner=me", nil, &out, true); err != nil {
		return nil, err
	}
	return out.Instances, nil
}

// Instance reads one instance.
func (c *Client) Instance(ctx context.Context, id int64) (Instance, error) {
	var out struct {
		Instances Instance `json:"instances"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/instances/%d/", id), nil, &out, true); err != nil {
		return Instance{}, err
	}
	if out.Instances.ID == 0 {
		return Instance{}, fmt.Errorf("%w: instance %d", ErrInstanceGone, id)
	}
	return out.Instances, nil
}

// FindByLabel returns the account's instance with this label.
func (c *Client) FindByLabel(ctx context.Context, label string) (Instance, error) {
	is, err := c.Instances(ctx)
	if err != nil {
		return Instance{}, err
	}
	for _, i := range is {
		if i.Label == label {
			return i, nil
		}
	}
	return Instance{}, fmt.Errorf("vast: no instance labelled %q", label)
}

// Stop stops compute billing and keeps the disk, so a Start skips the
// image pull and the downloads.
func (c *Client) Stop(ctx context.Context, id int64) error { return c.setState(ctx, id, "stopped") }

// Start restarts a stopped instance on the same disk.
func (c *Client) Start(ctx context.Context, id int64) error { return c.setState(ctx, id, "running") }

func (c *Client) setState(ctx context.Context, id int64, state string) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/instances/%d/", id), map[string]any{"state": state}, nil, true)
}

// Destroy deletes an instance and its disk. It cannot be undone.
func (c *Client) Destroy(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/instances/%d/", id), nil, nil, true)
}

// Logs is the tail of the instance's container log. vast.ai answers with a
// URL that fills in a few seconds later.
func (c *Client) Logs(ctx context.Context, id int64, tail int) (string, error) {
	var out struct {
		ResultURL string `json:"result_url"`
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/instances/request_logs/%d/", id),
		map[string]string{"tail": fmt.Sprint(tail)}, &out, true); err != nil {
		return "", err
	}
	if out.ResultURL == "" {
		return "", fmt.Errorf("vast: no log URL for instance %d", id)
	}
	for attempt := 0; attempt < 6; attempt++ {
		if !sleep(ctx, c.Poll) {
			return "", ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, out.ResultURL, nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && len(body) > 0 {
			return string(body), nil
		}
	}
	return "", fmt.Errorf("vast: logs for instance %d not ready yet, try again", id)
}

// Credit is the account's balance in dollars.
func (c *Client) Credit(ctx context.Context) (float64, error) {
	var out struct {
		Current struct {
			Credit float64 `json:"credit"`
		} `json:"current"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/current/invoices/", nil, &out, true); err != nil {
		return 0, err
	}
	return out.Current.Credit, nil
}

// AddSSHKey adds a public key to the account; instances rented after it
// accept it.
func (c *Client) AddSSHKey(ctx context.Context, pub string) error {
	pub = strings.TrimSpace(pub)
	if !strings.HasPrefix(pub, "ssh-") && !strings.HasPrefix(pub, "ecdsa-") {
		return errors.New("vast: not an SSH public key")
	}
	return c.do(ctx, http.MethodPost, "/ssh/", map[string]any{"ssh_key": pub}, nil, true)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, auth bool) error {
	if auth && c.Key == "" {
		return ErrNoKey
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	where := method + " " + strings.SplitN(path, "?", 2)[0]
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		err := fmt.Errorf("vast: %s: %s: %s", where, resp.Status, msg)
		if resp.StatusCode == http.StatusNotFound || strings.Contains(msg, "no_such_instance") {
			return fmt.Errorf("%w: %v", ErrInstanceGone, err)
		}
		return err
	}
	var refused struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(data, &refused) == nil && refused.Success != nil && !*refused.Success {
		err := fmt.Errorf("vast: %s: %s: %s", where, refused.Error, refused.Msg)
		if refused.Error == "resources_unavailable" {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return err
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("vast: %s: %w", where, err)
	}
	return nil
}
