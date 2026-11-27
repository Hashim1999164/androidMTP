package fs

import (
	"bytes"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-mtpfs/mtp"
)

const (
	noParent     = 0xFFFFFFFF
	blockSize    = 4096
	rootIno      = 1
	storageShift = 33
)

const (
	modeDir uint32 = 0x4000
	modeReg uint32 = 0x8000
)

// Stat is the file metadata the Finder driver asks for.
type Stat struct {
	Ino   uint64
	Mode  uint32
	Size  int64
	Mtime int64
}

// Statfs is the volume size the Finder driver asks for.
type Statfs struct {
	Bsize  uint64
	Blocks uint64
	Bavail uint64
}

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
	kind     nodeKind
	ino      uint64
	name     string
	storage  uint32
	handle   uint32
	parent   string
	size     int64
	mtime    time.Time
	isDir    bool
	fetched  bool     // directory listing cached
	children []string // child basenames in listing order
}

type openFile struct {
	path    string
	node    *node
	buf     *bytes.Buffer
	dirty   bool
	android bool
	editing bool
}

// FS exposes an MTP device to the Android MTP file system driver.
type FS struct {
	dev     *mtp.Device
	sids    []uint32
	opts    Options
	uid     uint32
	gid     uint32
	mu      sync.Mutex
	nodes   map[string]*node
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
		kind:    kindRoot,
		ino:     rootIno,
		name:    "/",
		isDir:   true,
		mtime:   time.Now(),
		fetched: true,
	}
	var names []string
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
		names = append(names, name)
	}
	fs.nodes["/"].children = names
	return fs, nil
}

func (fs *FS) Init() {
	log.Printf("androidmtp fs ready (android_ext=%v storages=%d cache=on)", fs.android, len(fs.sids))
}

func (fs *FS) Destroy() {}

func (fs *FS) Statfs(path string, stat *Statfs) int {
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
	if total == 0 {
		total = blockSize
	}
	stat.Blocks = total / blockSize
	stat.Bavail = free / blockSize
	return 0
}

func (fs *FS) Getattr(p string, stat *Stat, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	fs.fillStat(n, stat)
	return 0
}

func (fs *FS) fillStat(n *node, stat *Stat) {
	stat.Ino = n.ino
	stat.Mtime = n.mtime.Unix()
	if n.isDir {
		stat.Mode = modeDir | 0755
	} else {
		stat.Mode = modeReg | 0644
		stat.Size = n.size
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
		return -int(syscall.ENOTDIR), ^uint64(0)
	}
	// Warm directory cache once on open — Finder then hits cache for readdir/getattr storms.
	if _, errc := fs.ensureFetchedLocked(p, n); errc != 0 {
		return errc, ^uint64(0)
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
	fill func(name string, stat *Stat, ofst int64) bool,
	ofst int64,
	fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n, errc := fs.resolveLocked(p)
	if errc != 0 {
		return errc
	}
	if !n.isDir {
		return -int(syscall.ENOTDIR)
	}
	children, errc := fs.ensureFetchedLocked(p, n)
	if errc != 0 {
		return errc
	}
	fill(".", nil, 0)
	fill("..", nil, 0)
	for _, ch := range children {
		if strings.HasPrefix(ch.name, ".") {
			continue
		}
		st := Stat{}
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
		return -int(syscall.EISDIR), ^uint64(0)
	}
	fs.nextFH++
	fh := fs.nextFH
	// A writable open is not a write. Finder opens files while drawing icons,
	// and marking the handle dirty here deleted the phone file on close.
	fs.opens[fh] = &openFile{path: p, node: n, android: fs.android}
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
		return -int(syscall.EPERM), ^uint64(0)
	}
	info := mtp.ObjectInfo{
		StorageID:        parent.storage,
		ObjectFormat:     mtp.OFC_Undefined,
		ParentObject:     parent.handle,
		Filename:         name,
		CompressedSize:   0,
		ModificationDate: time.Now(),
	}
	child := &node{
		kind:    kindFile,
		name:    name,
		storage: parent.storage,
		parent:  parentPath,
		size:    0,
		mtime:   time.Now(),
	}
	if fs.android {
		_, _, handle, err := fs.dev.SendObjectInfo(parent.storage, parent.handle, &info)
		if err != nil {
			log.Printf("SendObjectInfo: %v", err)
			return -int(syscall.EIO), ^uint64(0)
		}
		if err := fs.dev.SendObject(bytes.NewReader(nil), 0); err != nil {
			_ = fs.dev.DeleteObject(handle)
			handle = 0
		}
		child.handle = handle
		child.ino = uint64(handle)<<1 | 1
	} else {
		child.ino = uint64(time.Now().UnixNano())
	}
	fs.nodes[p] = child
	fs.invalidateDirLocked(parentPath)
	fs.nextFH++
	fh := fs.nextFH
	fs.opens[fh] = &openFile{path: p, node: child, buf: &bytes.Buffer{}, dirty: true, android: fs.android}
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
	if !of.dirty || of.buf == nil || of.buf.Len() == 0 {
		return 0
	}
	return fs.flushWriteLocked(of)
}

func (fs *FS) flushWriteLocked(of *openFile) int {
	data := of.buf.Bytes()
	n := of.node
	parent, ok := fs.nodes[n.parent]
	if !ok {
		return -int(syscall.EIO)
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
		return -int(syscall.EIO)
	}
	if err := fs.dev.SendObject(bytes.NewReader(data), int64(len(data))); err != nil {
		log.Printf("SendObject flush: %v", err)
		return -int(syscall.EIO)
	}
	n.handle = handle
	n.size = int64(len(data))
	n.ino = uint64(handle)<<1 | 1
	n.mtime = time.Now()
	of.dirty = false
	fs.invalidateDirLocked(n.parent)
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
		return -int(syscall.EIO)
	}
	return copy(buf, b.Bytes())
}

func (fs *FS) Write(p string, buf []byte, off int64, fh uint64) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	of := fs.opens[fh]
	if of == nil {
		return -int(syscall.EBADF)
	}
	n := of.node

	if fs.android && n.handle != 0 && (of.buf == nil || (!of.dirty && of.buf.Len() == 0)) {
		if !of.editing {
			if err := fs.dev.AndroidBeginEditObject(n.handle); err != nil {
				log.Printf("BeginEdit: %v", err)
				return -int(syscall.EIO)
			}
			of.editing = true
		}
		if err := fs.dev.AndroidSendPartialObject(n.handle, off, uint32(len(buf)), bytes.NewReader(buf)); err != nil {
			log.Printf("SendPartial: %v", err)
			return -int(syscall.EIO)
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
			return -int(syscall.EIO)
		}
		if err := fs.dev.AndroidTruncate(n.handle, size); err != nil {
			_ = fs.dev.AndroidEndEditObject(n.handle)
			return -int(syscall.EIO)
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
		return -int(syscall.EPERM)
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
		return -int(syscall.EIO)
	}
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
	fs.invalidateDirLocked(parentPath)
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
		return -int(syscall.EISDIR)
	}
	parent := n.parent
	if n.handle != 0 {
		if err := fs.dev.DeleteObject(n.handle); err != nil {
			log.Printf("DeleteObject: %v", err)
			return -int(syscall.EIO)
		}
	}
	delete(fs.nodes, p)
	fs.invalidateDirLocked(parent)
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
		return -int(syscall.EPERM)
	}
	children, errc := fs.ensureFetchedLocked(p, n)
	if errc != 0 {
		return errc
	}
	if len(children) > 0 {
		return -int(syscall.ENOTEMPTY)
	}
	parent := n.parent
	if err := fs.dev.DeleteObject(n.handle); err != nil {
		return -int(syscall.EIO)
	}
	delete(fs.nodes, p)
	fs.invalidateDirLocked(parent)
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
		return -int(syscall.EXDEV)
	}
	newName := path.Base(newpath)
	if n.handle != 0 {
		v := mtp.StringValue{Value: newName}
		if err := fs.dev.SetObjectPropValue(n.handle, mtp.OPC_ObjectFileName, &v); err != nil {
			log.Printf("rename: %v", err)
			return -int(syscall.EIO)
		}
	}
	parent := n.parent
	delete(fs.nodes, oldpath)
	n.name = newName
	fs.nodes[newpath] = n
	fs.invalidateDirLocked(parent)
	return 0
}

// invalidateDirLocked marks a directory listing stale so the next open/readdir refreshes once.
func (fs *FS) invalidateDirLocked(dir string) {
	if n, ok := fs.nodes[dir]; ok && n.isDir {
		n.fetched = false
		n.children = nil
	}
}

// ensureFetchedLocked returns children, fetching from the phone only if not cached.
func (fs *FS) ensureFetchedLocked(p string, n *node) ([]*node, int) {
	if n.kind == kindRoot {
		var out []*node
		for _, name := range n.children {
			if ch := fs.nodes["/"+name]; ch != nil {
				out = append(out, ch)
			}
		}
		return out, 0
	}
	if n.fetched {
		return fs.childNodesLocked(p, n), 0
	}
	return fs.fetchChildrenLocked(p, n)
}

func (fs *FS) childNodesLocked(p string, n *node) []*node {
	out := make([]*node, 0, len(n.children))
	for _, name := range n.children {
		cp := path.Join(p, name)
		if ch := fs.nodes[cp]; ch != nil {
			out = append(out, ch)
		}
	}
	return out
}

func (fs *FS) fetchChildrenLocked(p string, n *node) ([]*node, int) {
	start := time.Now()
	handles := mtp.Uint32Array{}
	if err := fs.dev.GetObjectHandles(n.storage, 0x0, n.handle, &handles); err != nil {
		log.Printf("GetObjectHandles: %v", err)
		return nil, -int(syscall.EIO)
	}

	// Drop previous children of this directory only.
	for _, name := range n.children {
		delete(fs.nodes, path.Join(p, name))
	}
	n.children = n.children[:0]

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
		// Defer expensive ObjectSize prop lookup until a real read needs it.
		if info.CompressedSize == 0xFFFFFFFF {
			size = 0 // unknown; Getattr/Open can refine later if needed
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
		n.children = append(n.children, info.Filename)
		out = append(out, child)
	}
	n.fetched = true
	log.Printf("listed %s (%d items) in %s", p, len(out), time.Since(start).Round(time.Millisecond))
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
		if _, errc := fs.ensureFetchedLocked(cur, curNode); errc != 0 {
			return nil, errc
		}
		next := path.Join(cur, part)
		found := fs.nodes[next]
		if found == nil {
			return nil, -int(syscall.ENOENT)
		}
		cur = next
		curNode = found
	}
	return curNode, 0
}
