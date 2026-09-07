package amt

import (
	"context"
	"fmt"
	"strings"

	"github.com/bmc-toolbox/common"
)

// Inventory is the hardware inventory read from a device's CIM classes.
//
// It carries both a bmc-toolbox/common.Device -- the shape the rest of the
// Tinkerbell tooling already speaks -- and a flat summary that is cheap to
// persist in an AMTDevice status and cheap to render as Redfish without
// touching the device again.
type Inventory struct {
	Device  *common.Device
	Summary Summary
}

// Summary is the digest persisted in status and served by the Redfish layer.
type Summary struct {
	Manufacturer    string
	Model           string
	SerialNumber    string
	BaseboardModel  string
	BaseboardSerial string
	BIOSVersion     string
	CPUCount        int
	CPUModel        string
	MemoryBytes     int64
	NICs            []NIC
}

// NIC is one host network interface.
type NIC struct {
	MACAddress string
	Name       string
}

// Inventory reads the device's hardware inventory.
//
// Every class is read independently and a failure in one is tolerated: AMT
// populates these unevenly across platforms and firmware versions, and a
// partial inventory is materially more useful than none. Only a total failure
// to read the chassis -- which carries the identifying model and serial -- is
// reported as an error.
func (c *Client) Inventory(ctx context.Context) (*Inventory, error) {
	device := common.NewDevice()
	inv := &Inventory{Device: &device}
	dev := inv.Device

	chassis, err := c.chassis(ctx)
	if err != nil {
		return nil, err
	}
	dev.Vendor = chassis.Manufacturer
	dev.Model = chassis.Model
	dev.Serial = chassis.SerialNumber
	inv.Summary.Manufacturer = chassis.Manufacturer
	inv.Summary.Model = chassis.Model
	inv.Summary.SerialNumber = chassis.SerialNumber

	if board := c.mainboard(ctx); board != nil {
		dev.Mainboard = board
		inv.Summary.BaseboardModel = board.Model
		inv.Summary.BaseboardSerial = board.Serial
	}

	if b := c.bios(ctx); b != nil {
		dev.BIOS = b
		inv.Summary.BIOSVersion = b.Firmware.Installed
	}

	dev.CPUs = c.cpus(ctx)
	inv.Summary.CPUCount = len(dev.CPUs)
	if len(dev.CPUs) > 0 {
		inv.Summary.CPUModel = dev.CPUs[0].Model
	}

	var totalMemory int64
	for _, m := range c.memory(ctx) {
		dev.Memory = append(dev.Memory, m)
		totalMemory += m.SizeBytes
	}
	inv.Summary.MemoryBytes = totalMemory

	for _, n := range c.nics(ctx) {
		inv.Summary.NICs = append(inv.Summary.NICs, n)
		dev.NICs = append(dev.NICs, &common.NIC{
			Common:   common.Common{Description: n.Name},
			ID:       n.Name,
			NICPorts: []*common.NICPort{{MacAddress: n.MACAddress}},
		})
	}

	return inv, nil
}

type chassisInfo struct {
	Manufacturer string
	Model        string
	SerialNumber string
}

func (c *Client) chassis(_ context.Context) (chassisInfo, error) {
	enum, err := c.msg.CIM.Chassis.Enumerate()
	if err != nil {
		return chassisInfo{}, fmt.Errorf("amt: enumerating chassis: %w", err)
	}
	pull, err := c.msg.CIM.Chassis.Pull(enum.Body.EnumerateResponse.EnumerationContext)
	if err != nil {
		return chassisInfo{}, fmt.Errorf("amt: reading chassis: %w", err)
	}
	items := pull.Body.PullResponse.PackageItems
	if len(items) == 0 {
		return chassisInfo{}, fmt.Errorf("amt: device reported no chassis")
	}
	return chassisInfo{
		Manufacturer: strings.TrimSpace(items[0].Manufacturer),
		Model:        strings.TrimSpace(items[0].Model),
		SerialNumber: strings.TrimSpace(items[0].SerialNumber),
	}, nil
}

func (c *Client) mainboard(_ context.Context) *common.Mainboard {
	enum, err := c.msg.CIM.Card.Enumerate()
	if err != nil {
		return nil
	}
	pull, err := c.msg.CIM.Card.Pull(enum.Body.EnumerateResponse.EnumerationContext)
	if err != nil || len(pull.Body.PullResponse.CardItems) == 0 {
		return nil
	}
	card := pull.Body.PullResponse.CardItems[0]
	return &common.Mainboard{
		Common: common.Common{
			Vendor:      strings.TrimSpace(card.Manufacturer),
			Model:       strings.TrimSpace(card.Model),
			Serial:      strings.TrimSpace(card.SerialNumber),
			Description: strings.TrimSpace(card.ElementName),
		},
	}
}

func (c *Client) bios(_ context.Context) *common.BIOS {
	resp, err := c.msg.CIM.BIOSElement.Get()
	if err != nil {
		return nil
	}
	b := resp.Body.BIOSElementGetResponse
	if b.Version == "" {
		return nil
	}
	return &common.BIOS{
		Common: common.Common{
			Vendor:      strings.TrimSpace(b.Manufacturer),
			Description: strings.TrimSpace(b.Name),
			Firmware:    &common.Firmware{Installed: strings.TrimSpace(b.Version)},
		},
	}
}

func (c *Client) cpus(_ context.Context) []*common.CPU {
	enum, err := c.msg.CIM.Processor.Enumerate()
	if err != nil {
		return nil
	}
	pull, err := c.msg.CIM.Processor.Pull(enum.Body.EnumerateResponse.EnumerationContext)
	if err != nil {
		return nil
	}

	var out []*common.CPU
	for _, p := range pull.Body.PullResponse.PackageItems {
		out = append(out, &common.CPU{
			Common: common.Common{
				Description: strings.TrimSpace(p.ElementName),
				Model:       strings.TrimSpace(p.ElementName),
				Metadata: map[string]string{
					"deviceID":         p.DeviceID,
					"maxClockSpeedMHz": fmt.Sprint(p.MaxClockSpeed),
					"currentClockMHz":  fmt.Sprint(p.CurrentClockSpeed),
					"family":           fmt.Sprint(p.Family),
					"stepping":         p.Stepping,
				},
			},
			ClockSpeedHz: int64(p.MaxClockSpeed) * 1_000_000,
		})
	}
	return out
}

func (c *Client) memory(_ context.Context) []*common.Memory {
	enum, err := c.msg.CIM.PhysicalMemory.Enumerate()
	if err != nil {
		return nil
	}
	pull, err := c.msg.CIM.PhysicalMemory.Pull(enum.Body.EnumerateResponse.EnumerationContext)
	if err != nil {
		return nil
	}

	var out []*common.Memory
	for _, m := range pull.Body.PullResponse.MemoryItems {
		if m.Capacity == 0 {
			continue
		}
		out = append(out, &common.Memory{
			Common: common.Common{
				Vendor:      strings.TrimSpace(m.Manufacturer),
				Serial:      strings.TrimSpace(m.SerialNumber),
				Description: strings.TrimSpace(m.ElementName),
			},
			PartNumber:   strings.TrimSpace(m.PartNumber),
			Slot:         strings.TrimSpace(m.BankLabel),
			SizeBytes:    int64(m.Capacity),
			ClockSpeedHz: int64(m.ConfiguredMemoryClockSpeed) * 1_000_000,
		})
	}
	return out
}

// nics reads CIM_EthernetPort.
//
// The MAC is not where CIM says it should be: AMT leaves PermanentAddress
// empty and reports the address in NetworkAddresses instead, without
// separators. Both are checked, and the result is normalised to the
// colon-separated lower-case form the Tinkerbell resources use.
func (c *Client) nics(_ context.Context) []NIC {
	enum, err := c.msg.CIM.EthernetPort.Enumerate()
	if err != nil {
		return nil
	}
	pull, err := c.msg.CIM.EthernetPort.Pull(enum.Body.EnumerateResponse.EnumerationContext)
	if err != nil {
		return nil
	}

	var out []NIC
	for _, p := range pull.Body.PullResponse.EthernetPortItems {
		mac := NormalizeMAC(p.PermanentAddress)
		for _, addr := range p.NetworkAddresses {
			if mac != "" {
				break
			}
			mac = NormalizeMAC(addr)
		}
		// An unpopulated port (a disabled wireless radio, typically) reports
		// an all-zero address. Surfacing it would put a bogus interface on
		// Hardware and collide with every other device that does the same.
		if mac == "" || isZeroMAC(mac) {
			continue
		}
		name := strings.TrimSpace(p.Description)
		if name == "" {
			name = strings.TrimSpace(p.ElementName)
		}
		out = append(out, NIC{MACAddress: mac, Name: name})
	}
	return out
}

// isZeroMAC reports whether a normalised MAC is all zeroes.
func isZeroMAC(mac string) bool { return mac == "00:00:00:00:00:00" }

// NormalizeMAC renders a MAC address as lower-case colon-separated octets.
// It accepts the bare 12-hex-digit form AMT reports as well as addresses that
// already carry ':' or '-' separators, and returns "" for anything else.
func NormalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
	if len(s) != 12 {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i : i+2])
	}
	return b.String()
}
