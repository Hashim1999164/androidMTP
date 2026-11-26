package fs

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-mtpfs/mtp"
	"github.com/winfsp/cgofuse/fuse"
)

const (
	noParent     = 0xFFFFFFFF
	blockSize    = 512
	rootIno      = 1
	storageShift = 33
)

// Options configure the MTP filesystem.
type Options struct {
	Android bool
}

type nodeKind int

const (
	kindRoot nodeKind = iota
	kindStorage
	kindDir
	kindFile
)

type node struct {
	kind    nodeKind
	ino     uint64
	name    string
	storage uint32
	handle  uint32 // MTP object handle; for storage roots = noParent
	parent  string // absolute parent path
	size    int64
	mtime   time.Time
	isDir   bool
}

type openFile struct {
	path    string
	node    *node
	buf     *bytes.Buffer // write buffer for classic / create
	dirty   bool
	android bool
	editing bool
}

// FS exposes an MTP device through cgofuse (FUSE-T on macOS).
type FS struct {
	fuse.FileSystemBase
	dev     *mtp.Device
	sids    []uint32
	opts    Options
	uid     uint32
	gid     uint32
	mu      sync.Mutex
	nodes   map[string]*node // path -> node
	opens   map[uint64]*openFile
	nextFH  uint64
	android bool
}

// New builds a filesystem for an already-configured MTP device.
func New(dev *mtp.Device, sids []uint32, opts Options) (*FS, error) {
	var di mtp.DeviceInfo
	if err := dev.GetDeviceInfo(&di); err != nil {
		return nil, err
	}
	android := opts.Android && strings.Contains(di.MTPExtension, "android.com")
	fs := &FS{
		dev:     dev,
		sids:    sids,
		opts:    opts,
		uid:     uint32(syscall.Getuid()),
		gid:     uint32(syscall.Getgid()),
		nodes:   map[string]*node{},
		opens:   map[uint64]*openFile{},
		android: android,
	}
	fs.nodes["/"] = &node{
		kind:  kindRoot,
		ino:   rootIno,
		name:  "/",
		isDir: true,
		mtime: time.Now(),
	}
	for _, sid := range sids {
		var info mtp.StorageInfo
		if err := dev.GetStorageInfo(sid, &info); err != nil {
			return nil, err
		}
		name := info.StorageDescription
		if name == "" {
			name = fmt.Sprintf("Storage-%x", sid)
		}
		p := "/" + name
		fs.nodes[p] = &node{
			kind:    kindStorage,
			ino:     uint64(sid) << storageShift,
			name:    name,
			storage: sid,
			handle:  noParent,
			parent:  "/",
			isDir:   true,
			mtime:   time.Now(),
		}
	}
	return fs, nil
}

func (fs *FS) Init() {
	log.Printf("androidmtp fs ready (android_ext=%v storages=%d)", fs.android, len(fs.sids))
}

func (fs *FS) Destroy() {}

func (fs *FS) Statfs(path string, stat *fuse.Statfs_t) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var total, free uint64
	for _, sid := range fs.sids {
		var info mtp.StorageInfo
		if err := fs.dev.GetStorageInfo(sid, &info); err != nil {
			continue
		}
		total += info.MaxCapability
		free += info.FreeSpaceInBytes
	}
	stat.Bsize = blockSize
	stat.Frsize = blockSize
	if total == 0 {
		total = blockSize
	}
	stat.Blocks = total / blockSize
	stat.Bfree = free / blockSize
	stat.Bavail = free / blockSize
	stat.Namemax = 255
	return 0
}

func (fs *FS) Getattr(p string, stat *fuse.Stat_t, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	fs.fillStat(n, stat)
	return 0
}

func (fs *FS) fillStat(n *node, stat *fuse.Stat_t) {
	stat.Ino = n.ino
	stat.Nlink = 1
	stat.Uid = fs.uid
	stat.Gid = fs.gid
	stat.Blksize = blockSize
	ts := fuse.NewTimespec(n.mtime)
	stat.Atim = ts
	stat.Mtim = ts
	stat.Ctim = ts
	stat.Birthtim = ts
	if n.isDir {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Nlink = 2
	} else {
		stat.Mode = fuse.S_IFREG | 0644
		stat.Size = n.size
		stat.Blocks = (n.size + blockSize - 1) / blockSize
	}
}

func (fs *FS) Opendir(p string) (int, uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc, ^uint64(0)
	}
	if !n.isDir {
		return -fuse.ENOTDIR, ^uint64(0)
	}
	fs.nextFH++
	fh := fs.nextFH
	fs.opens[fh] = &openFile{path: p, node: n}
	return 0, fh
}

func (fs *FS) Releasedir(p string, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.opens, fh)
	return 0
}

func (fs *FS) Readdir(p string,
	fill func(name string, stat *fuse.Stat_t, ofst int64) bool,
	ofst int64,
	fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	if !n.isDir {
		return -fuse.ENOTDIR
	}
	fill(".", nil, 0)
	fill("..", nil, 0)

	children, errc := fs.listChildrenLocked(p, n)
	if errc != 0 {
		return errc
	}
	for _, ch := range children {
		st := fuse.Stat_t{}
		fs.fillStat(ch, &st)
		if !fill(ch.name, &st, 0) {
			break
		}
	}
	return 0
}

func (fs *FS) Open(p string, flags int) (int, uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc, ^uint64(0)
	}
	if n.isDir {
		return -fuse.EISDIR, ^uint64(0)
	}
	fs.nextFH++
	fh := fs.nextFH
	of := &openFile{path: p, node: n, android: fs.android}
	if flags&fuse.O_WRONLY != 0 || flags&fuse.O_RDWR != 0 || flags&fuse.O_APPEND != 0 {
		of.buf = &bytes.Buffer{}
		of.dirty = true
	}
	fs.opens[fh] = of
	return 0, fh
}

func (fs *FS) Create(p string, flags int, mode uint32) (int, uint64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parentPath := path.Dir(p)
	name := path.Base(p)
	parent, errc := fs.resolveLocked(parentPath)
	if errc != 0 {
		return errc, ^uint64(0)
	}
	if !parent.isDir || parent.kind == kindRoot {
		return -fuse.EPERM, ^uint64(0)
	}
	// Create zero-byte object on device so handle exists; writers flush on release.
	info := mtp.ObjectInfo{
		StorageID:        parent.storage,
		ObjectFormat:     mtp.OFC_Undefined,
		ParentObject:     parent.handle,
		Filename:         name,
		CompressedSize:   0,
		ModificationDate: time.Now(),
	}
	if fs.android {
		// Android prefers partial edit after create; start with empty object.
		_, _, handle, err := fs.dev.SendObjectInfo(parent.storage, parent.handle, &info)
		if err != nil {
			log.Printf("SendObjectInfo: %v", err)
			return -fuse.EIO, ^uint64(0)
		}
		if err := fs.dev.SendObject(bytes.NewReader(nil), 0); err != nil {
			// Some devices reject 0-byte SendObject; delete and use classic buffer-only create.
			_ = fs.dev.DeleteObject(handle)
			handle = 0
		}
		child := &node{
			kind:    kindFile,
			ino:     uint64(handle)<<1 | 1,
			name:    name,
			storage: parent.storage,
			handle:  handle,
			parent:  parentPath,
			size:    0,
			mtime:   time.Now(),
		}
		fs.nodes[p] = child
		fs.nextFH++
		fh := fs.nextFH
		of := &openFile{path: p, node: child, buf: &bytes.Buffer{}, dirty: true, android: fs.android}
		fs.opens[fh] = of
		return 0, fh
	}

	child := &node{
		kind:    kindFile,
		ino:     uint64(time.Now().UnixNano()),
		name:    name,
		storage: parent.storage,
		handle:  0,
		parent:  parentPath,
		size:    0,
		mtime:   time.Now(),
	}
	fs.nodes[p] = child
	fs.nextFH++
	fh := fs.nextFH
	fs.opens[fh] = &openFile{path: p, node: child, buf: &bytes.Buffer{}, dirty: true}
	return 0, fh
}

func (fs *FS) Release(p string, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	of := fs.opens[fh]
	delete(fs.opens, fh)
	if of == nil {
		return 0
	}
	if of.editing {
		_ = fs.dev.AndroidEndEditObject(of.node.handle)
		of.editing = false
	}
	if !of.dirty || of.buf == nil {
		return 0
	}
	return fs.flushWriteLocked(of)
}

func (fs *FS) flushWriteLocked(of *openFile) int {
	data := of.buf.Bytes()
	n := of.node
	parent, ok := fs.nodes[n.parent]
	if !ok {
		return -fuse.EIO
	}
	if n.handle != 0 {
		_ = fs.dev.DeleteObject(n.handle)
		n.handle = 0
	}
	info := mtp.ObjectInfo{
		StorageID:        n.storage,
		ObjectFormat:     mtp.OFC_Undefined,
		ParentObject:     parent.handle,
		Filename:         n.name,
		CompressedSize:   uint32(len(data)),
		ModificationDate: time.Now(),
	}
	if len(data) > 0xFFFFFFFF {
		info.CompressedSize = 0xFFFFFFFF
	}
	_, _, handle, err := fs.dev.SendObjectInfo(n.storage, parent.handle, &info)
	if err != nil {
		log.Printf("SendObjectInfo flush: %v", err)
		return -fuse.EIO
	}
	if err := fs.dev.SendObject(bytes.NewReader(data), int64(len(data))); err != nil {
		log.Printf("SendObject flush: %v", err)
		return -fuse.EIO
	}
	n.handle = handle
	n.size = int64(len(data))
	n.ino = uint64(handle)<<1 | 1
	n.mtime = time.Now()
	of.dirty = false
	return 0
}

func (fs *FS) Read(p string, buf []byte, off int64, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	of := fs.opens[fh]
	var n *node
	if of != nil {
		n = of.node
		if of.buf != nil && of.dirty && of.buf.Len() > 0 {
			data := of.buf.Bytes()
			if off >= int64(len(data)) {
				return 0
			}
			return copy(buf, data[off:])
		}
	} else {
		var errc int
		n, errc = fs.resolveLocked(p)
		if errc != 0 {
			return errc
		}
	}
	if n.handle == 0 {
		return 0
	}
	if off >= n.size {
		return 0
	}
	want := int64(len(buf))
	if off+want > n.size {
		want = n.size - off
	}
	var b bytes.Buffer
	var err error
	if fs.android {
		err = fs.dev.AndroidGetPartialObject64(n.handle, &b, off, uint32(want))
	} else {
		// Classic: pull whole object (slow but compatible).
		var full bytes.Buffer
		err = fs.dev.GetObject(n.handle, &full)
		if err == nil {
			data := full.Bytes()
			if off < int64(len(data)) {
				end := off + want
				if end > int64(len(data)) {
					end = int64(len(data))
				}
				b.Write(data[off:end])
			}
		}
	}
	if err != nil {
		log.Printf("read %s: %v", p, err)
		return -fuse.EIO
	}
	return copy(buf, b.Bytes())
}

func (fs *FS) Write(p string, buf []byte, off int64, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	of := fs.opens[fh]
	if of == nil {
		return -fuse.EBADF
	}
	n := of.node

	// Prefer Android partial writes when the object already exists on device
	// and we are not in whole-buffer create mode.
	if fs.android && n.handle != 0 && (of.buf == nil || (!of.dirty && of.buf.Len() == 0)) {
		if !of.editing {
			if err := fs.dev.AndroidBeginEditObject(n.handle); err != nil {
				log.Printf("BeginEdit: %v", err)
				return -fuse.EIO
			}
			of.editing = true
		}
		if err := fs.dev.AndroidSendPartialObject(n.handle, off, uint32(len(buf)), bytes.NewReader(buf)); err != nil {
			log.Printf("SendPartial: %v", err)
			return -fuse.EIO
		}
		if off+int64(len(buf)) > n.size {
			n.size = off + int64(len(buf))
		}
		return len(buf)
	}

	if of.buf == nil {
		of.buf = &bytes.Buffer{}
	}
	of.dirty = true
	need := int(off) + len(buf)
	raw := of.buf.Bytes()
	if len(raw) < need {
		grown := make([]byte, need)
		copy(grown, raw)
		of.buf = bytes.NewBuffer(grown)
		raw = of.buf.Bytes()
	}
	copy(raw[off:], buf)
	if int64(need) > n.size {
		n.size = int64(need)
	}
	return len(buf)
}

func (fs *FS) Truncate(p string, size int64, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	if fs.android && n.handle != 0 {
		if err := fs.dev.AndroidBeginEditObject(n.handle); err != nil {
			return -fuse.EIO
		}
		if err := fs.dev.AndroidTruncate(n.handle, size); err != nil {
			_ = fs.dev.AndroidEndEditObject(n.handle)
			return -fuse.EIO
		}
		_ = fs.dev.AndroidEndEditObject(n.handle)
		n.size = size
		return 0
	}
	if of := fs.opens[fh]; of != nil && of.buf != nil {
		if int64(of.buf.Len()) > size {
			of.buf.Truncate(int(size))
		} else {
			for int64(of.buf.Len()) < size {
				of.buf.WriteByte(0)
			}
		}
		of.dirty = true
		n.size = size
		return 0
	}
	n.size = size
	return 0
}

func (fs *FS) Mkdir(p string, mode uint32) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	parentPath := path.Dir(p)
	name := path.Base(p)
	parent, errc := fs.resolveLocked(parentPath)
	if errc != 0 {
		return errc
	}
	if parent.kind == kindRoot {
		return -fuse.EPERM
	}
	info := mtp.ObjectInfo{
		StorageID:        parent.storage,
		ObjectFormat:     mtp.OFC_Association,
		ParentObject:     parent.handle,
		Filename:         name,
		AssociationType:  mtp.AT_GenericFolder,
		ModificationDate: time.Now(),
	}
	_, _, handle, err := fs.dev.SendObjectInfo(parent.storage, parent.handle, &info)
	if err != nil {
		log.Printf("mkdir SendObjectInfo: %v", err)
		return -fuse.EIO
	}
	// Association objects typically need an empty SendObject on some stacks.
	_ = fs.dev.SendObject(bytes.NewReader(nil), 0)
	fs.nodes[p] = &node{
		kind:    kindDir,
		ino:     uint64(handle) << 1,
		name:    name,
		storage: parent.storage,
		handle:  handle,
		parent:  parentPath,
		isDir:   true,
		mtime:   time.Now(),
	}
	return 0
}

func (fs *FS) Unlink(p string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	if n.isDir {
		return -fuse.EISDIR
	}
	if n.handle != 0 {
		if err := fs.dev.DeleteObject(n.handle); err != nil {
			log.Printf("DeleteObject: %v", err)
			return -fuse.EIO
		}
	}
	delete(fs.nodes, p)
	return 0
}

func (fs *FS) Rmdir(p string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	if !n.isDir || n.kind == kindRoot || n.kind == kindStorage {
		return -fuse.EPERM
	}
	children, errc := fs.listChildrenLocked(p, n)
	if errc != 0 {
		return errc
	}
	if len(children) > 0 {
		return -fuse.ENOTEMPTY
	}
	if err := fs.dev.DeleteObject(n.handle); err != nil {
		return -fuse.EIO
	}
	delete(fs.nodes, p)
	return 0
}

func (fs *FS) Rename(oldpath string, newpath string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(oldpath)
	if errc != 0 {
		return errc
	}
	if path.Dir(oldpath) != path.Dir(newpath) {
		return -fuse.EXDEV // MTP rarely supports cross-folder move
	}
	newName := path.Base(newpath)
	if n.handle != 0 {
		v := mtp.StringValue{Value: newName}
		if err := fs.dev.SetObjectPropValue(n.handle, mtp.OPC_ObjectFileName, &v); err != nil {
			log.Printf("rename: %v", err)
			return -fuse.EIO
		}
	}
	delete(fs.nodes, oldpath)
	n.name = newName
	fs.nodes[newpath] = n
	return 0
}

func (fs *FS) listChildrenLocked(p string, n *node) ([]*node, int) {
	if n.kind == kindRoot {
		var out []*node
		for _, sid := range fs.sids {
			for _, child := range fs.nodes {
				if child.kind == kindStorage && child.storage == sid {
					out = append(out, child)
				}
			}
		}
		return out, 0
	}

	handles := mtp.Uint32Array{}
	parentHandle := n.handle
	if err := fs.dev.GetObjectHandles(n.storage, 0x0, parentHandle, &handles); err != nil {
		log.Printf("GetObjectHandles: %v", err)
		return nil, -fuse.EIO
	}

	// Drop stale children under this path.
	prefix := p
	if prefix != "/" {
		prefix += "/"
	}
	for key, child := range fs.nodes {
		if child.parent == p {
			delete(fs.nodes, key)
		}
	}

	var out []*node
	for _, handle := range handles.Values {
		var info mtp.ObjectInfo
		if err := fs.dev.GetObjectInfo(handle, &info); err != nil {
			continue
		}
		if info.Filename == "" {
			continue
		}
		childPath := path.Join(p, info.Filename)
		size := int64(info.CompressedSize)
		if info.CompressedSize == 0xFFFFFFFF {
			var val mtp.Uint64Value
			if err := fs.dev.GetObjectPropValue(handle, mtp.OPC_ObjectSize, &val); err == nil {
				size = int64(val.Value)
			}
		}
		isDir := info.ObjectFormat == mtp.OFC_Association
		child := &node{
			ino:     uint64(handle) << 1,
			name:    info.Filename,
			storage: n.storage,
			handle:  handle,
			parent:  p,
			size:    size,
			mtime:   info.ModificationDate,
			isDir:   isDir,
		}
		if isDir {
			child.kind = kindDir
		} else {
			child.kind = kindFile
			child.ino |= 1
		}
		fs.nodes[childPath] = child
		out = append(out, child)
	}
	return out, 0
}

func (fs *FS) resolveLocked(p string) (*node, int) {
	p = path.Clean(p)
	if p == "" {
		p = "/"
	}
	if n, ok := fs.nodes[p]; ok {
		return n, 0
	}
	// Walk from root, listing as needed.
	if p == "/" {
		return fs.nodes["/"], 0
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	cur := "/"
	curNode := fs.nodes["/"]
	for _, part := range parts {
		if part == "" {
			continue
		}
		children, errc := fs.listChildrenLocked(cur, curNode)
		if errc != 0 {
			return nil, errc
		}
		found := (*node)(nil)
		for _, ch := range children {
			if ch.name == part {
				found = ch
				break
			}
		}
		if found == nil {
			return nil, -fuse.ENOENT
		}
		if cur == "/" {
			cur = "/" + part
		} else {
			cur = cur + "/" + part
		}
		curNode = found
	}
	return curNode, 0
}

// Mount starts the FUSE-T host and blocks until unmount.
func Mount(fsys *FS, mountpoint string, volname string, allowOther bool) error {
	host := fuse.NewFileSystemHost(fsys)
	opts := []string{
		"-o", "fsname=androidmtp",
		"-o", "volname=" + volname,
		"-o", "local",
		"-o", "noappledouble",
		"-o", "noapplexattr",
		"-s", // single-threaded — MTP is not concurrency-safe
	}
	if allowOther {
		opts = append(opts, "-o", "allow_other")
	}
	// Ensure fuse-t helper (go-nfsv4) is findable.
	pathEnv := os.Getenv("PATH")
	extra := []string{
		"/usr/local/bin",
		filepathJoinHome(".fuse-t/usr/local/bin"),
	}
	os.Setenv("PATH", strings.Join(append(extra, pathEnv), string(os.PathListSeparator)))

	ok := host.Mount(mountpoint, opts)
	if !ok {
		return fmt.Errorf("fuse mount failed — install FUSE-T: brew install --cask fuse-t")
	}
	return nil
}

func filepathJoinHome(rel string) string {
	h, err := os.UserHomeDir()
	if err != nil {
		return rel
	}
	return path.Join(h, rel)
}
