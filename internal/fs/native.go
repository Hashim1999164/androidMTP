package fs

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

// Wire protocol shared with driver/AndroidMTPFS.swift.
// Each message is a little-endian u32 length, then that many bytes.
// Request body: u16 op, payload. Response body: i32 errno, payload.
const (
	opStatFS   uint16 = 1
	opGetattr  uint16 = 2
	opReaddir  uint16 = 3
	opOpen     uint16 = 4
	opClose    uint16 = 5
	opRead     uint16 = 6
	opWrite    uint16 = 7
	opMkdir    uint16 = 8
	opCreate   uint16 = 9
	opUnlink   uint16 = 10
	opRmdir    uint16 = 11
	opRename   uint16 = 12
	opTruncate uint16 = 13
)

const nativeModuleID = "org.hashim.androidmtp.fsmodule"

func newContainerID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// NativeModuleRegistered reports whether the Android MTP FSKit driver is installed.
func NativeModuleRegistered() bool {
	out, err := exec.Command("pluginkit", "-m", "-i", nativeModuleID).Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "+") || strings.TrimSpace(string(out)) != ""
}

// MountNative serves this filesystem to the Android MTP FSKit driver and mounts it.
// It blocks until the volume is unmounted. The driver is a local volume, not a network share.
func MountNative(fsys *FS, mountpoint, volname string) error {
	if !NativeModuleRegistered() {
		return fmt.Errorf("android MTP file system driver is not installed")
	}
	// The file-system extension is sandboxed. It can open a TCP port on
	// localhost, which is how the volume reaches this process.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	go serveNative(ln, fsys)

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	session := filepath.Join(os.TempDir(), "androidmtp-"+id+".session")
	body := "tcp=" + ln.Addr().String() + "\nid=" + newContainerID() + "\nvolume=" + strings.ReplaceAll(volname, "\n", " ") + "\n"
	if err := os.WriteFile(session, []byte(body), 0o644); err != nil {
		return err
	}
	defer os.Remove(session)

	cmd := exec.Command("mount", "-t", "androidmtp", session, mountpoint)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("native mount: %s", msg)
	}
	for {
		time.Sleep(500 * time.Millisecond)
		ok, _ := mountedPath(mountpoint)
		if !ok {
			return nil
		}
	}
}

func mountedPath(path string) (bool, error) {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "on" && fields[i+1] == path {
				return true, nil
			}
		}
	}
	return false, nil
}

func serveNative(ln net.Listener, fsys *FS) {
	opens := map[string]uint64{}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handleNative(conn, fsys, opens)
		conn.Close()
	}
}

func handleNative(conn net.Conn, fsys *FS, opens map[string]uint64) {
	for {
		op, payload, err := readReq(conn)
		if err != nil {
			return
		}
		errno, resp := dispatch(fsys, opens, op, payload)
		if err := writeResp(conn, errno, resp); err != nil {
			return
		}
	}
}

func dispatch(fsys *FS, opens map[string]uint64, op uint16, payload []byte) (int32, []byte) {
	switch op {
	case opStatFS:
		st := fuse.Statfs_t{}
		if rc := fsys.Statfs("/", &st); rc < 0 {
			return int32(-rc), nil
		}
		var b [16]byte
		total := uint64(st.Blocks) * uint64(st.Bsize)
		free := uint64(st.Bavail) * uint64(st.Bsize)
		binary.LittleEndian.PutUint64(b[0:8], total)
		binary.LittleEndian.PutUint64(b[8:16], free)
		return 0, b[:]
	case opGetattr:
		p, _ := takeCString(payload)
		return getattrBytes(fsys, p)
	case opReaddir:
		p, _ := takeCString(payload)
		return readdirBytes(fsys, p)
	case opOpen:
		p, rest := takeCString(payload)
		write := len(rest) > 0 && rest[0] == 1
		flags := 0
		if write {
			flags = fuse.O_RDWR
		}
		rc, fh := fsys.Open(p, flags)
		if rc < 0 {
			return int32(-rc), nil
		}
		opens[p] = fh
		return 0, nil
	case opClose:
		p, _ := takeCString(payload)
		fh := opens[p]
		delete(opens, p)
		if rc := fsys.Release(p, fh); rc < 0 {
			return int32(-rc), nil
		}
		return 0, nil
	case opRead:
		p, rest := takeCString(payload)
		if len(rest) < 12 {
			return int32(fuse.EINVAL), nil
		}
		off := int64(binary.LittleEndian.Uint64(rest[0:8]))
		n := int(binary.LittleEndian.Uint32(rest[8:12]))
		if n < 0 || n > 4<<20 {
			return int32(fuse.EINVAL), nil
		}
		buf := make([]byte, n)
		rc := fsys.Read(p, buf, off, opens[p])
		if rc < 0 {
			return int32(-rc), nil
		}
		return 0, buf[:rc]
	case opWrite:
		p, rest := takeCString(payload)
		if len(rest) < 12 {
			return int32(fuse.EINVAL), nil
		}
		off := int64(binary.LittleEndian.Uint64(rest[0:8]))
		n := int(binary.LittleEndian.Uint32(rest[8:12]))
		if n < 0 || 12+n > len(rest) {
			return int32(fuse.EINVAL), nil
		}
		data := rest[12 : 12+n]
		fh, ok := opens[p]
		if !ok {
			rc, newFH := fsys.Open(p, fuse.O_RDWR)
			if rc < 0 {
				return int32(-rc), nil
			}
			fh = newFH
			opens[p] = fh
		}
		rc := fsys.Write(p, data, off, fh)
		if rc < 0 {
			return int32(-rc), nil
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(rc))
		return 0, b[:]
	case opMkdir:
		p, _ := takeCString(payload)
		if rc := fsys.Mkdir(p, 0o755); rc < 0 {
			return int32(-rc), nil
		}
		return getattrBytes(fsys, p)
	case opCreate:
		p, _ := takeCString(payload)
		rc, fh := fsys.Create(p, fuse.O_RDWR, 0o644)
		if rc < 0 {
			return int32(-rc), nil
		}
		opens[p] = fh
		return getattrBytes(fsys, p)
	case opUnlink:
		p, _ := takeCString(payload)
		if rc := fsys.Unlink(p); rc < 0 {
			return int32(-rc), nil
		}
		return 0, nil
	case opRmdir:
		p, _ := takeCString(payload)
		if rc := fsys.Rmdir(p); rc < 0 {
			return int32(-rc), nil
		}
		return 0, nil
	case opRename:
		from, rest := takeCString(payload)
		to, _ := takeCString(rest)
		if rc := fsys.Rename(from, to); rc < 0 {
			return int32(-rc), nil
		}
		return 0, nil
	case opTruncate:
		p, rest := takeCString(payload)
		if len(rest) < 8 {
			return int32(fuse.EINVAL), nil
		}
		size := int64(binary.LittleEndian.Uint64(rest[:8]))
		if rc := fsys.Truncate(p, size, opens[p]); rc < 0 {
			return int32(-rc), nil
		}
		return getattrBytes(fsys, p)
	default:
		return int32(fuse.ENOSYS), nil
	}
}

func getattrBytes(fsys *FS, p string) (int32, []byte) {
	st := fuse.Stat_t{}
	if rc := fsys.Getattr(p, &st, 0); rc < 0 {
		return int32(-rc), nil
	}
	var b [25]byte
	if st.Mode&fuse.S_IFDIR != 0 {
		b[0] = 1
	}
	ino := st.Ino
	if ino < 16 {
		ino += 16
	}
	binary.LittleEndian.PutUint64(b[1:9], ino)
	binary.LittleEndian.PutUint64(b[9:17], uint64(st.Size))
	binary.LittleEndian.PutUint64(b[17:25], uint64(st.Mtim.Sec))
	return 0, b[:]
}

func readdirBytes(fsys *FS, p string) (int32, []byte) {
	var entries []byte
	var n uint32
	rc := fsys.Readdir(p, func(name string, st *fuse.Stat_t, _ int64) bool {
		if name == "." || name == ".." {
			return true
		}
		raw := []byte(name)
		if len(raw) > 65535 {
			raw = raw[:65535]
		}
		var hdr [2 + 1 + 8 + 8 + 8]byte
		binary.LittleEndian.PutUint16(hdr[0:2], uint16(len(raw)))
		isDir := byte(0)
		var ino, size, mtime uint64
		if st != nil {
			if st.Mode&fuse.S_IFDIR != 0 {
				isDir = 1
			}
			ino = st.Ino
			if ino < 16 {
				ino += 16
			}
			size = uint64(st.Size)
			mtime = uint64(st.Mtim.Sec)
		}
		chunk := make([]byte, 2+len(raw)+1+24)
		binary.LittleEndian.PutUint16(chunk[0:2], uint16(len(raw)))
		copy(chunk[2:], raw)
		off := 2 + len(raw)
		chunk[off] = isDir
		binary.LittleEndian.PutUint64(chunk[off+1:off+9], ino)
		binary.LittleEndian.PutUint64(chunk[off+9:off+17], size)
		binary.LittleEndian.PutUint64(chunk[off+17:off+25], mtime)
		entries = append(entries, chunk...)
		n++
		_ = hdr
		return true
	}, 0, 0)
	if rc < 0 {
		return int32(-rc), nil
	}
	out := make([]byte, 4+len(entries))
	binary.LittleEndian.PutUint32(out[0:4], n)
	copy(out[4:], entries)
	return 0, out
}

func takeCString(b []byte) (string, []byte) {
	for i, c := range b {
		if c == 0 {
			return string(b[:i]), b[i+1:]
		}
	}
	return string(b), nil
}

func readReq(r io.Reader) (uint16, []byte, error) {
	var nbuf [4]byte
	if _, err := io.ReadFull(r, nbuf[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(nbuf[:])
	if n < 2 || n > 8<<20 {
		return 0, nil, fmt.Errorf("bad frame %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint16(buf[:2])
	return op, buf[2:], nil
}

func writeResp(w io.Writer, errno int32, payload []byte) error {
	body := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(body[0:4], uint32(errno))
	copy(body[4:], payload)
	var nbuf [4]byte
	binary.LittleEndian.PutUint32(nbuf[:], uint32(len(body)))
	if _, err := w.Write(nbuf[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
