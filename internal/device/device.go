package device

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanwen/go-mtpfs/mtp"
	"github.com/hanwen/usb"
)

// Info describes a detected MTP device.
type Info struct {
	Index        int
	ID           string
	Manufacturer string
	Product      string
	Serial       string
	Model        string
	VendorID     uint16
	ProductID    uint16
	Bus          uint8
	Address      uint8
	Storages     []Storage
}

// Storage is an MTP storage area on a device.
type Storage struct {
	ID          uint32
	Description string
	VolumeID    string
	FreeBytes   uint64
	TotalBytes  uint64
}

// Key identifies one phone across replugs. The USB serial is stable. Phones
// that do not report a serial fall back to vendor, product, and name.
func (info Info) Key() string {
	if s := strings.TrimSpace(info.Serial); s != "" {
		return s
	}
	if info.VendorID != 0 || info.ProductID != 0 {
		return fmt.Sprintf("%04x:%04x:%s", info.VendorID, info.ProductID, info.Product)
	}
	if info.ID != "" {
		return info.ID
	}
	return fmt.Sprintf("index-%d", info.Index)
}

// OnBus reports whether this phone is still attached. IORegistry updates when
// the cable moves, even while an MTP session has the device open. A failed
// registry read is treated as still attached so a glitch cannot hide the disk.
func OnBus(info Info) bool {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-w", "0", "-l").Output()
	if err != nil {
		return true
	}
	text := string(out)
	if serial := strings.TrimSpace(info.Serial); serial != "" {
		return strings.Contains(text, `"USB Serial Number" = "`+serial+`"`)
	}
	if info.VendorID != 0 {
		wantVendor := fmt.Sprintf(`"idVendor" = %d`, info.VendorID)
		wantProduct := fmt.Sprintf(`"idProduct" = %d`, info.ProductID)
		for _, block := range strings.Split(text, "+-o ") {
			if strings.Contains(block, wantVendor) && strings.Contains(block, wantProduct) {
				return true
			}
		}
		return false
	}
	if info.Product != "" {
		return strings.Contains(text, `"USB Product Name" = "`+info.Product+`"`)
	}
	return false
}

// Find locates a phone by vendor and product id without opening it.
// A replug keeps the same ids and gets a new USB address. ok is false when
// the bus could not be read.
func Find(vendor, product uint16) (bus, addr uint8, found, ok bool) {
	if vendor == 0 && product == 0 {
		return 0, 0, false, false
	}
	c := usb.NewContext()
	defer c.Exit()
	list, err := c.GetDeviceList()
	if err != nil {
		return 0, 0, false, false
	}
	if len(list) == 0 {
		return 0, 0, false, true
	}
	defer list.Done()
	for _, dev := range list {
		dd, err := dev.GetDeviceDescriptor()
		if err != nil {
			continue
		}
		if dd.IdVendor == vendor && dd.IdProduct == product {
			return dev.GetBusNumber(), dev.GetDeviceAddress(), true, true
		}
	}
	return 0, 0, false, true
}

// ProbeConnected finds every Android that is in File Transfer mode.
// A USB gadget with the same three endpoints (a hub, a LAN adapter, a
// keyboard) is ignored unless it answers the MTP handshake. The check is
// the protocol, not a list of brands.
func ProbeConnected(filter string) ([]Info, error) {
	c := usb.NewContext()
	raw, err := mtp.FindDevices(c)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(filter)
	if err != nil {
		for _, d := range raw {
			d.Done()
		}
		return nil, err
	}

	var out []Info
	for _, d := range raw {
		if err := d.Open(); err != nil {
			d.Done()
			continue
		}
		manufacturer, product, serial := d.Identity()
		_ = d.Close()
		d.Done()
		if manufacturer == "" && product == "" && serial == "" {
			continue
		}
		parts := []string{}
		for _, s := range []string{manufacturer, product, serial} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		info := Info{
			ID:           strings.Join(parts, " "),
			Manufacturer: manufacturer,
			Product:      product,
			Serial:       serial,
		}
		if info.Product == "" {
			info.Product = manufacturer
		}
		if info.Product == "" {
			info.Product = "Android"
		}
		if filter != "" && re.FindString(info.ID) == "" {
			continue
		}
		info.Index = len(out)
		out = append(out, info)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no usable MTP devices found")
	}
	return out, nil
}

// ListInfos enumerates connected MTP devices with storage metadata (for `list` / doctor).
func ListInfos(filter string) ([]Info, error) {
	return listInfos(filter, true)
}

func listInfos(filter string, deep bool) ([]Info, error) {
	c := usb.NewContext()
	raw, err := mtp.FindDevices(c)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("no MTP devices found — unlock the phone and choose File Transfer / MTP")
	}

	re, err := regexp.Compile(filter)
	if err != nil {
		for _, d := range raw {
			d.Done()
		}
		return nil, err
	}

	var out []Info
	idx := 0
	for _, d := range raw {
		if err := d.Open(); err != nil {
			d.Done()
			continue
		}
		id, err := d.ID()
		if err != nil {
			_ = d.Close()
			d.Done()
			continue
		}
		if filter != "" && re.FindString(id) == "" {
			_ = d.Close()
			d.Done()
			continue
		}

		info := Info{Index: idx, ID: id}
		parts := strings.Fields(id)
		if len(parts) >= 1 {
			info.Manufacturer = parts[0]
		}
		if len(parts) >= 2 {
			info.Product = parts[1]
		}
		if len(parts) >= 3 {
			info.Serial = strings.Join(parts[2:], " ")
		}

		if deep {
			if err := d.Configure(); err == nil {
				var di mtp.DeviceInfo
				if err := d.GetDeviceInfo(&di); err == nil {
					info.Model = di.Model
					if info.Manufacturer == "" {
						info.Manufacturer = di.Manufacturer
					}
				}
				if sids, err := SelectStorages(d, ""); err == nil {
					for _, sid := range sids {
						var si mtp.StorageInfo
						if err := d.GetStorageInfo(sid, &si); err != nil {
							continue
						}
						info.Storages = append(info.Storages, Storage{
							ID:          sid,
							Description: si.StorageDescription,
							VolumeID:    si.VolumeLabel,
							FreeBytes:   si.FreeSpaceInBytes,
							TotalBytes:  si.MaxCapability,
						})
					}
				}
			}
		}

		out = append(out, info)
		idx++
		_ = d.Close()
		d.Done()
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no usable MTP devices found")
	}
	return out, nil
}

// Open selects a device by empty (first), numeric index, or ID regex; configures MTP; returns storage IDs.
func Open(selector string, usbTimeoutMS int) (*mtp.Device, []uint32, mtp.DeviceInfo, error) {
	pattern := selector
	if selector != "" {
		if n, err := strconv.Atoi(selector); err == nil {
			infos, err := ListInfos("")
			if err != nil {
				return nil, nil, mtp.DeviceInfo{}, err
			}
			if n < 0 || n >= len(infos) {
				return nil, nil, mtp.DeviceInfo{}, fmt.Errorf("device index %d out of range (0..%d)", n, len(infos)-1)
			}
			// Prefer serial when present for a unique match.
			pattern = regexp.QuoteMeta(infos[n].ID)
		}
	}

	dev, err := mtp.SelectDevice(pattern)
	if err != nil {
		return nil, nil, mtp.DeviceInfo{}, err
	}
	if usbTimeoutMS > 0 {
		dev.Timeout = usbTimeoutMS
	}
	if err := dev.Configure(); err != nil {
		_ = dev.Close()
		dev.Done()
		return nil, nil, mtp.DeviceInfo{}, fmt.Errorf("configure MTP session: %w", err)
	}

	sids, err := SelectStorages(dev, "")
	if err != nil {
		_ = dev.Close()
		dev.Done()
		return nil, nil, mtp.DeviceInfo{}, err
	}
	if len(sids) == 0 {
		_ = dev.Close()
		dev.Done()
		return nil, nil, mtp.DeviceInfo{}, fmt.Errorf("device has no hierarchical MTP storages")
	}

	var di mtp.DeviceInfo
	if err := dev.GetDeviceInfo(&di); err != nil {
		_ = dev.Close()
		dev.Done()
		return nil, nil, mtp.DeviceInfo{}, err
	}
	return dev, sids, di, nil
}

// FriendlyName builds a Finder-safe volume name.
func FriendlyName(di mtp.DeviceInfo, fallback string) string {
	name := strings.TrimSpace(di.Model)
	if name == "" {
		name = strings.TrimSpace(fallback)
	}
	if name == "" {
		name = "Android"
	}
	replacer := strings.NewReplacer("/", "-", ":", "-", "\\", "-", "\x00", "")
	name = replacer.Replace(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
