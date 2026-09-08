package redfish

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeClient struct {
	powerState string
	powerErr   error

	resetCalls  []string
	bootCalls   []string
	cleared     int
	insertCalls []string
	ejected     int

	actionErr error
}

func (c *fakeClient) PowerState(context.Context) (string, error) {
	return c.powerState, c.powerErr
}

func (c *fakeClient) Reset(_ context.Context, t string) error {
	c.resetCalls = append(c.resetCalls, t)
	return c.actionErr
}

func (c *fakeClient) SetBootOverride(_ context.Context, t string) error {
	c.bootCalls = append(c.bootCalls, t)
	return c.actionErr
}

func (c *fakeClient) ClearBootOverride(context.Context) error {
	c.cleared++
	return c.actionErr
}

func (c *fakeClient) InsertVirtualMedia(_ context.Context, url string, _ bool) error {
	c.insertCalls = append(c.insertCalls, url)
	return c.actionErr
}

func (c *fakeClient) EjectVirtualMedia(context.Context) error {
	c.ejected++
	return c.actionErr
}

func (c *fakeClient) Close() error { return nil }

type fakeStore struct {
	devices    []Device
	client     *fakeClient
	connectErr error
	listErr    error
}

func (s *fakeStore) List(context.Context) ([]Device, error) {
	return s.devices, s.listErr
}

func (s *fakeStore) Get(_ context.Context, id string) (Device, error) {
	if s.listErr != nil {
		return Device{}, s.listErr
	}
	for _, d := range s.devices {
		if d.ID == id {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

func (s *fakeStore) Connect(context.Context, string) (Client, error) {
	if s.connectErr != nil {
		return nil, s.connectErr
	}
	return s.client, nil
}

const testID = "1b72a5ce-4584-9e6a-9f12-88aedd753da0"

func testDevice() Device {
	return Device{
		ID:           testID,
		Name:         "nuc1",
		Host:         "10.0.0.160",
		Manufacturer: "ASUSTeK COMPUTER INC.",
		Model:        "NUC15CRHV7",
		SerialNumber: "TBARQK0038327AB",
		BIOSVersion:  "CRARLV57.0024.2025.0507.1536",
		CPUCount:     1,
		MemoryBytes:  103079215104,
		AMTVersion:   "18.1.18",
		ControlMode:  "Admin",
		Phase:        "Registered",
		Reachable:    true,
		Capabilities: Capabilities{PXE: true, HardDrive: true, CD: true, UEFIHTTPS: true},
	}
}

func newTestServer(t *testing.T, store Store) *httptest.Server {
	t.Helper()
	s := &Server{
		Store:       store,
		ServiceUUID: "00000000-0000-0000-0000-000000000001",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if resp.Header.Get("Content-Type") == "application/json" {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("decoding %s: %v", path, err)
		}
	}
	return resp.StatusCode, body
}

func send(t *testing.T, srv *httptest.Server, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	return resp.StatusCode
}

func TestServiceRoot(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeStore{})

	for _, path := range []string{Base, Base + "/"} {
		status, body := get(t, srv, path)
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d", path, status)
		}
		if body["@odata.id"] != Base {
			t.Errorf("@odata.id = %v", body["@odata.id"])
		}
		if body["Id"] != "RootService" {
			t.Errorf("Id = %v", body["Id"])
		}
		// The aggregation service is the whole point of this being an
		// aggregator rather than a BMC emulator.
		if body["AggregationService"] == nil {
			t.Error("service root does not advertise AggregationService")
		}
	}
}

func TestSystemCollectionDoesNotContactDevices(t *testing.T) {
	t.Parallel()

	// A nil client means any Connect() use would panic. The collection must
	// render from stored state alone, otherwise listing a large fleet would
	// fan out to every device.
	store := &fakeStore{devices: []Device{testDevice()}, client: nil}
	srv := newTestServer(t, store)

	status, body := get(t, srv, Base+"/Systems")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if count, _ := body["Members@odata.count"].(float64); count != 1 {
		t.Errorf("Members@odata.count = %v, want 1", body["Members@odata.count"])
	}
	members, _ := body["Members"].([]any)
	if len(members) != 1 {
		t.Fatalf("got %d members", len(members))
	}
	first, _ := members[0].(map[string]any)
	if want := Base + "/Systems/" + testID; first["@odata.id"] != want {
		t.Errorf("member @odata.id = %v, want %v", first["@odata.id"], want)
	}
}

func TestSystemRendersInventoryAndLivePower(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		devices: []Device{testDevice()},
		client:  &fakeClient{powerState: "On"},
	}
	srv := newTestServer(t, store)

	status, body := get(t, srv, Base+"/Systems/"+testID)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if body["Model"] != "NUC15CRHV7" {
		t.Errorf("Model = %v", body["Model"])
	}
	if body["PowerState"] != "On" {
		t.Errorf("PowerState = %v, want the live value", body["PowerState"])
	}
	if body["UUID"] != testID {
		t.Errorf("UUID = %v, want the platform GUID", body["UUID"])
	}

	mem, _ := body["MemorySummary"].(map[string]any)
	if gib, _ := mem["TotalSystemMemoryGiB"].(float64); gib != 96 {
		t.Errorf("TotalSystemMemoryGiB = %v, want 96", mem["TotalSystemMemoryGiB"])
	}

	boot, _ := body["Boot"].(map[string]any)
	allowed, _ := boot["BootSourceOverrideEnabled@Redfish.AllowableValues"].([]any)
	for _, v := range allowed {
		if v == "Continuous" {
			t.Error("Continuous must not be advertised: AMT clears boot parameters once consumed")
		}
	}
}

// An unreachable device must still be served, with a health status saying so.
// Omitting it would make a broken machine indistinguishable from one that was
// never enrolled.
func TestUnreachableDeviceIsServedAsCritical(t *testing.T) {
	t.Parallel()

	dev := testDevice()
	dev.Reachable = false
	store := &fakeStore{devices: []Device{dev}, client: nil}
	srv := newTestServer(t, store)

	status, body := get(t, srv, Base+"/Systems/"+testID)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if body["PowerState"] != "Unknown" {
		t.Errorf("PowerState = %v, want Unknown for an unreachable device", body["PowerState"])
	}
	st, _ := body["Status"].(map[string]any)
	if st["Health"] != "Critical" || st["State"] != "UnavailableOffline" {
		t.Errorf("Status = %v, want Critical/UnavailableOffline", st)
	}
}

func TestUnknownDeviceIs404(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t, &fakeStore{})

	status, body := get(t, srv, Base+"/Systems/does-not-exist")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if body["error"] == nil {
		t.Error("404 response has no Redfish error body")
	}
}

func TestResetTranslatesToClient(t *testing.T) {
	t.Parallel()

	client := &fakeClient{powerState: "On"}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/Actions/ComputerSystem.Reset",
		`{"ResetType":"ForceRestart"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if len(client.resetCalls) != 1 || client.resetCalls[0] != "ForceRestart" {
		t.Errorf("resetCalls = %v", client.resetCalls)
	}
}

func TestResetRejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/Actions/ComputerSystem.Reset",
		`{"ResetType":"PushPowerButton"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if len(client.resetCalls) != 0 {
		t.Error("an unsupported reset type reached the device")
	}
}

func TestPatchBootOverride(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPatch, Base+"/Systems/"+testID,
		`{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Pxe"}}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if len(client.bootCalls) != 1 || client.bootCalls[0] != "Pxe" {
		t.Errorf("bootCalls = %v", client.bootCalls)
	}
}

// Continuous must be refused rather than quietly downgraded to Once: a caller
// expecting a persistent override would be surprised at the second boot.
func TestPatchRejectsContinuous(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPatch, Base+"/Systems/"+testID,
		`{"Boot":{"BootSourceOverrideEnabled":"Continuous","BootSourceOverrideTarget":"Pxe"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if len(client.bootCalls) != 0 {
		t.Error("a Continuous override reached the device")
	}
}

func TestPatchDisabledClearsOverride(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPatch, Base+"/Systems/"+testID,
		`{"Boot":{"BootSourceOverrideEnabled":"Disabled"}}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if client.cleared != 1 {
		t.Errorf("cleared = %d, want 1", client.cleared)
	}
}

func TestPatchRejectsUnsupportedTarget(t *testing.T) {
	t.Parallel()

	dev := testDevice()
	dev.Capabilities.CD = false
	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{dev}, client: client})

	status := send(t, srv, http.MethodPatch, Base+"/Systems/"+testID,
		`{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Cd"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a capability the device lacks", status)
	}
	if len(client.bootCalls) != 0 {
		t.Error("an unsupported boot target reached the device")
	}
}

// Virtual media must be absent, not broken, on a device without UEFI HTTPS
// boot. Accepting an image the device will ignore is the worst outcome: every
// call succeeds and the machine quietly does not boot it.
func TestVirtualMediaAbsentWithoutUefiHTTPS(t *testing.T) {
	t.Parallel()

	dev := testDevice()
	dev.Capabilities.UEFIHTTPS = false
	srv := newTestServer(t, &fakeStore{devices: []Device{dev}, client: &fakeClient{}})

	_, system := get(t, srv, Base+"/Systems/"+testID)
	if _, present := system["VirtualMedia"]; present {
		t.Error("VirtualMedia advertised on a device without UEFI HTTPS boot")
	}

	for _, path := range []string{
		Base + "/Systems/" + testID + "/VirtualMedia",
		Base + "/Systems/" + testID + "/VirtualMedia/Cd",
	} {
		if status, _ := get(t, srv, path); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
}

func TestVirtualMediaPresentWithUefiHTTPS(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: &fakeClient{}})

	_, system := get(t, srv, Base+"/Systems/"+testID)
	if _, present := system["VirtualMedia"]; !present {
		t.Error("VirtualMedia not advertised on a capable device")
	}
	status, media := get(t, srv, Base+"/Systems/"+testID+"/VirtualMedia/Cd")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if media["TransferProtocolType"] != "HTTPS" {
		t.Errorf("TransferProtocolType = %v, want HTTPS", media["TransferProtocolType"])
	}
}

func TestInsertMedia(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia",
		`{"Image":"https://images.example.com/hook.iso"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if len(client.insertCalls) != 1 {
		t.Fatalf("insertCalls = %v", client.insertCalls)
	}
}

// http:// is the dangerous case: AMT accepts every WS-Man call in the boot
// sequence and then silently boots normally, so it must be refused up front.
func TestInsertMediaRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	for _, body := range []string{
		`{"Image":"http://images.example.com/hook.iso"}`,
		`{"Image":"ftp://images.example.com/hook.iso"}`,
		`{"Image":""}`,
		`{"Image":"https://x/y.iso","TransferProtocolType":"HTTP"}`,
	} {
		status := send(t, srv, http.MethodPost,
			Base+"/Systems/"+testID+"/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia", body)
		if status != http.StatusBadRequest {
			t.Errorf("body %s gave status %d, want 400", body, status)
		}
	}
	if len(client.insertCalls) != 0 {
		t.Errorf("a rejected image reached the device: %v", client.insertCalls)
	}
}

func TestEjectMedia(t *testing.T) {
	t.Parallel()

	client := &fakeClient{}
	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: client})

	status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia", `{}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if client.ejected != 1 {
		t.Errorf("ejected = %d, want 1", client.ejected)
	}
}

// An unreachable device is a device problem, not a service fault; reporting it
// as 500 would send operators looking in the wrong place.
func TestConnectFailureIsServiceUnavailable(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, &fakeStore{
		devices:    []Device{testDevice()},
		connectErr: errors.New("dial timeout"),
	})

	status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestAggregationSourceReportsEnrollmentState(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: &fakeClient{}})

	status, body := get(t, srv, Base+"/AggregationService/AggregationSources/"+testID)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	oem, _ := body["Oem"].(map[string]any)
	intel, _ := oem["IntelAMT"].(map[string]any)
	if intel["EnrollmentPhase"] != "Registered" {
		t.Errorf("EnrollmentPhase = %v", intel["EnrollmentPhase"])
	}
	if intel["Protocol"] != "WSMAN" {
		t.Errorf("Protocol = %v, want WSMAN", intel["Protocol"])
	}
}

func TestChassisAndManager(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, &fakeStore{devices: []Device{testDevice()}, client: &fakeClient{}})

	status, chassis := get(t, srv, Base+"/Chassis/"+testID)
	if status != http.StatusOK {
		t.Fatalf("chassis status = %d", status)
	}
	if chassis["SerialNumber"] != "TBARQK0038327AB" {
		t.Errorf("chassis SerialNumber = %v", chassis["SerialNumber"])
	}

	status, manager := get(t, srv, Base+"/Managers/"+testID)
	if status != http.StatusOK {
		t.Fatalf("manager status = %d", status)
	}
	if manager["FirmwareVersion"] != "18.1.18" {
		t.Errorf("manager FirmwareVersion = %v", manager["FirmwareVersion"])
	}
	if manager["ManagerType"] != "BMC" {
		t.Errorf("ManagerType = %v, want BMC", manager["ManagerType"])
	}
}

// Get and Connect must admit exactly the same devices. A device Get returns
// but Connect cannot find would appear in the aggregator with every action
// failing as unreachable.
func TestStoreAdmissionIsConsistent(t *testing.T) {
	t.Parallel()

	// A device that is verified but whose inventory has not been collected.
	dev := testDevice()
	dev.Manufacturer, dev.Model, dev.SerialNumber = "", "", ""
	store := &fakeStore{devices: []Device{dev}, client: &fakeClient{powerState: "On"}}
	srv := newTestServer(t, store)

	if status, _ := get(t, srv, Base+"/Systems/"+testID); status != http.StatusOK {
		t.Fatalf("system status = %d, want 200 for a device without inventory", status)
	}
	// And an action against it must reach the device rather than 404/503.
	if status := send(t, srv, http.MethodPost,
		Base+"/Systems/"+testID+"/Actions/ComputerSystem.Reset",
		`{"ResetType":"On"}`); status != http.StatusNoContent {
		t.Errorf("reset status = %d, want 204", status)
	}
}

// An empty UUID is not a valid Redfish service identity.
func TestServiceRootOmitsEmptyUUID(t *testing.T) {
	t.Parallel()

	s := &Server{
		Store: &fakeStore{},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	_, body := get(t, srv, Base)
	if _, present := body["UUID"]; present {
		t.Error("service root emitted a UUID property with no UUID configured")
	}

	_, body = get(t, newTestServer(t, &fakeStore{}), Base)
	if body["UUID"] != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("UUID = %v, want the configured value", body["UUID"])
	}
}

func TestODataVersionHeader(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, &fakeStore{})
	resp, err := srv.Client().Get(srv.URL + Base)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("OData-Version"); got != "4.0" {
		t.Errorf("OData-Version = %q, want 4.0", got)
	}
}

func TestBytesToGiB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{-1, 0},
		{103079215104, 96},
		{1 << 30, 1},
		{(1 << 30) + (1 << 29), 2}, // rounds to nearest
	}
	for _, tc := range tests {
		if got := bytesToGiB(tc.in); got != tc.want {
			t.Errorf("bytesToGiB(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
