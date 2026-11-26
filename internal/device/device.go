package device

import (
	"fmt"
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

// ProbeConnected is a light USB check for the plug-and-play watcher.
// It does not start an MTP session, and it ignores Apple devices such as iPhone.
func ProbeConnected(filter string) ([]Info, error) {
	c := usb.NewContext()
	list, err := c.GetDeviceList()
	if err != nil {
		return nil, err
	}
	if len(list) > 0 {
		defer list.Done()
	}

	re, err := regexp.Compile(filter)
	if err != nil {
		return nil, err
	}

	var out []Info
	for _, dev := range list {
		info, ok := androidMTPInfo(dev)
		if !ok {
			continue
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

// androidMTPInfo reports a phone that is actually in File Transfer mode.
// Apple's PTP interface looks similar on the bus and is skipped.
func androidMTPInfo(dev *usb.Device) (Info, bool) {
	dd, err := dev.GetDeviceDescriptor()
	if err != nil || dd.IdVendor == 0x05ac {
		return Info{}, false
	}
	var ifaceString byte
	found := false
	for i := byte(0); i < dd.NumConfigurations; i++ {
		cfg, err := dev.GetConfigDescriptor(i)
		if err != nil {
			continue
		}
		for _, iface := range cfg.Interfaces {
			for _, alt := range iface.AltSetting {
				if !mtpEndpoints(alt.EndPoints) {
					continue
				}
				ifaceString = alt.InterfaceStringIndex
				found = true
			}
		}
	}
	if !found || ifaceString == 0 {
		return Info{}, false
	}

	h, err := dev.Open()
	if err != nil {
		return Info{}, false
	}
	defer h.Close()

	s, err := h.GetStringDescriptorASCII(ifaceString)
	if err != nil || !strings.Contains(strings.ToUpper(s), "MTP") {
		return Info{}, false
	}

	var parts []string
	for _, idx := range []byte{dd.Manufacturer, dd.Product, dd.SerialNumber} {
		if idx == 0 {
			continue
		}
		s, err := h.GetStringDescriptorASCII(idx)
		if err != nil || s == "" {
			continue
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return Info{}, false
	}
	id := strings.Join(parts, " ")
	info := Info{ID: id, Product: parts[0]}
	if len(parts) >= 2 {
		info.Manufacturer = parts[0]
		info.Product = parts[1]
	}
	if len(parts) >= 3 {
		info.Serial = strings.Join(parts[2:], " ")
	}
	return info, true
}

func mtpEndpoints(eps []usb.EndpointDescriptor) bool {
	var inBulk, outBulk, inIntr bool
	for i := range eps {
		e := &eps[i]
		switch {
		case e.Direction() == usb.ENDPOINT_IN && e.TransferType() == usb.TRANSFER_TYPE_INTERRUPT:
			inIntr = true
		case e.Direction() == usb.ENDPOINT_IN && e.TransferType() == usb.TRANSFER_TYPE_BULK:
			inBulk = true
		case e.Direction() == usb.ENDPOINT_OUT && e.TransferType() == usb.TRANSFER_TYPE_BULK:
			outBulk = true
		}
	}
	return inBulk && outBulk && inIntr
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
