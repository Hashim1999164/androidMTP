import Darwin
import ExtensionFoundation
import Foundation
import FSKit
import os

private let fsLog = Logger(subsystem: "org.hashim.androidmtp.fsmodule", category: "mount")

// Speaks the little-endian socket protocol implemented by internal/fs/native.go.
private final class MTPConnection {
    private let fd: Int32
    private let lock = NSLock()

    init(tcp: String) throws {
        let parts = tcp.split(separator: ":", maxSplits: 1).map(String.init)
        guard parts.count == 2, let port = UInt16(parts[1]) else { throw POSIXError(.EINVAL) }
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        if fd < 0 { throw POSIXError(.EIO) }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        parts[0].withCString { host in
            addr.sin_addr.s_addr = inet_addr(host)
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                connect(fd, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        if rc != 0 {
            let err = errno
            close(fd)
            throw POSIXError(POSIXErrorCode(rawValue: err) ?? .EIO)
        }
        var tv = timeval(tv_sec: 12, tv_usec: 0)
        _ = withUnsafePointer(to: &tv) { ptr in
            setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, ptr, socklen_t(MemoryLayout<timeval>.size))
        }
        _ = withUnsafePointer(to: &tv) { ptr in
            setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, ptr, socklen_t(MemoryLayout<timeval>.size))
        }
        self.fd = fd
    }

    init(unixPath: String) throws {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        if fd < 0 { throw POSIXError(.EIO) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let max = MemoryLayout.size(ofValue: addr.sun_path) - 1
        unixPath.withCString { src in
            withUnsafeMutablePointer(to: &addr.sun_path) { dst in
                _ = strncpy(UnsafeMutableRawPointer(dst).assumingMemoryBound(to: CChar.self), src, max)
            }
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                connect(fd, sa, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if rc != 0 {
            let err = errno
            close(fd)
            throw POSIXError(POSIXErrorCode(rawValue: err) ?? .EIO)
        }
        self.fd = fd
    }

    deinit { close(fd) }

    func call(op: UInt16, payload: Data = Data()) throws -> Data {
        lock.lock()
        defer { lock.unlock() }
        var body = Data()
        var opLE = op.littleEndian
        body.append(Data(bytes: &opLE, count: 2))
        body.append(payload)
        var n = UInt32(body.count).littleEndian
        var frame = Data(bytes: &n, count: 4)
        frame.append(body)
        try writeAll(frame)
        let respLen = try readU32()
        if respLen < 4 || respLen > 8 << 20 { throw POSIXError(.EIO) }
        let resp = try readExact(Int(respLen))
        let errno = resp.withUnsafeBytes { raw in raw.load(as: Int32.self) }
        if errno != 0 { throw NSError(domain: NSPOSIXErrorDomain, code: Int(errno), userInfo: nil) }
        return resp.dropFirst(4)
    }

    private func writeAll(_ data: Data) throws {
        try data.withUnsafeBytes { raw in
            var off = 0
            let base = raw.bindMemory(to: UInt8.self).baseAddress!
            while off < raw.count {
                let w = write(fd, base.advanced(by: off), raw.count - off)
                if w < 0 { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
                off += w
            }
        }
    }

    private func readExact(_ count: Int) throws -> Data {
        var data = Data(count: count)
        try data.withUnsafeMutableBytes { raw in
            var off = 0
            let base = raw.bindMemory(to: UInt8.self).baseAddress!
            while off < count {
                let r = read(fd, base.advanced(by: off), count - off)
                if r == 0 { throw POSIXError(.ECONNRESET) }
                if r < 0 { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
                off += r
            }
        }
        return data
    }

    private func readU32() throws -> UInt32 {
        let d = try readExact(4)
        return d.withUnsafeBytes { $0.load(as: UInt32.self) }
    }
}

private func cString(_ s: String) -> Data {
    var d = Data(s.utf8)
    d.append(0)
    return d
}

private struct Attr {
    var isDir: Bool
    var ino: UInt64
    var size: UInt64
    var mtime: Int64
}

private func parseAttr(_ data: Data) throws -> Attr {
    if data.count < 25 { throw POSIXError(.EIO) }
    return data.withUnsafeBytes { raw in
        let dir = raw.load(fromByteOffset: 0, as: UInt8.self) != 0
        let ino = raw.loadUnaligned(fromByteOffset: 1, as: UInt64.self)
        let size = raw.loadUnaligned(fromByteOffset: 9, as: UInt64.self)
        let mtime = raw.loadUnaligned(fromByteOffset: 17, as: Int64.self)
        return Attr(isDir: dir, ino: ino, size: size, mtime: mtime)
    }
}

private final class MTPItem: FSItem {
    let path: String
    var itemID: UInt64
    var parentID: UInt64
    var directory: Bool
    var attr: Attr
    init(path: String, itemID: UInt64, parentID: UInt64, directory: Bool, attr: Attr) {
        self.path = path
        self.itemID = itemID
        self.parentID = parentID
        self.directory = directory
        self.attr = attr
        super.init()
    }
}

// FSItem.Identifier's Swift initializer only accepts the three reserved values
// (invalid=0, parent-of-root=1, root=2) and returns nil for a real inode.
// Falling back to `.rootDirectory` makes every phone file look like the volume
// root, and Finder's getattrlist then waits forever on that vnode.
private func fileIdentifier(_ raw: UInt64, isRoot: Bool = false) -> FSItem.Identifier {
    if isRoot || raw == FSItem.Identifier.rootDirectory.rawValue {
        return .rootDirectory
    }
    if raw == FSItem.Identifier.parentOfRoot.rawValue {
        return .parentOfRoot
    }
    let safe: UInt64 = raw < 3 ? raw &+ 16 : raw
    if let id = FSItem.Identifier(rawValue: safe), id.rawValue == safe {
        return id
    }
    return unsafeBitCast(safe, to: FSItem.Identifier.self)
}

private func childPath(_ dir: String, _ name: String) -> String {
    if dir == "/" { return "/" + name }
    return dir + "/" + name
}

private final class MTPVolume: FSVolume, FSVolume.Operations, FSVolume.Handler, FSVolume.OpenCloseOperations, FSVolume.ReadWriteOperations, FSVolume.XattrHandler {
    let rpc: MTPConnection
    private var items: [String: MTPItem] = [:]
    private var totalBytes: UInt64 = 0
    private var freeBytes: UInt64 = 0

    init(rpc: MTPConnection, name: String, id: UUID) {
        self.rpc = rpc
        super.init(volumeID: FSVolume.Identifier(uuid: id), volumeName: FSFileName(string: name))
    }

    var supportedVolumeCapabilities: FSVolume.SupportedCapabilities {
        let caps = FSVolume.SupportedCapabilities()
        caps.supports2TBFiles = true
        caps.supports64BitObjectIDs = true
        caps.supportsFastStatFS = true
        caps.doesNotSupportSettingFilePermissions = true
        caps.caseFormat = .sensitive
        return caps
    }

    var volumeStatistics: FSStatFSResult {
        let stat = FSStatFSResult(fileSystemTypeName: "androidmtp")
        stat.blockSize = 4096
        stat.totalBytes = totalBytes
        stat.availableBytes = freeBytes
        stat.freeBytes = freeBytes
        return stat
    }

    var maximumLinkCount: Int { 1 }
    var maximumNameLength: Int { 255 }
    var restrictsOwnershipChanges: Bool { true }
    var truncatesLongNames: Bool { false }
    var maximumFileSize: UInt64 { UInt64.max }

    func mount(options: FSTaskOptions, replyHandler reply: @escaping (Error?) -> Void) { reply(nil) }
    func unmount(replyHandler reply: @escaping () -> Void) { reply() }
    func synchronize(flags: FSSyncFlags, replyHandler reply: @escaping (Error?) -> Void) { reply(nil) }

    func activate(options: FSTaskOptions, replyHandler reply: @escaping (FSItem?, Error?) -> Void) {
        do {
            if let data = try? rpc.call(op: 1), data.count >= 16 {
                totalBytes = data.withUnsafeBytes { $0.load(as: UInt64.self) }
                freeBytes = data.withUnsafeBytes { $0.loadUnaligned(fromByteOffset: 8, as: UInt64.self) }
            }
            let attr = try parseAttr(rpc.call(op: 2, payload: cString("/")))
            let root = remember(path: "/", attr: attr, parentID: FSItem.Identifier.parentOfRoot.rawValue)
            let probe = FSItem.Identifier(rawValue: 100)
            fsLog.error("id probe 100 \(probe?.rawValue ?? 0, privacy: .public) nil=\(probe == nil, privacy: .public)")
            reply(root, nil)
        } catch {
            reply(nil, error)
        }
    }

    func activateVolume(options: FSTaskOptions, replyHandler reply: @escaping (FSActivateResult?, (any Error)?) -> Void) {
        activate(options: options) { item, err in
            if let err {
                reply(nil, err)
                return
            }
            guard let item else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(FSActivateResult(rootItem: item), nil)
        }
    }

    func deactivate(options: FSDeactivateOptions, replyHandler reply: @escaping (Error?) -> Void) {
        items.removeAll()
        reply(nil)
    }

    func deactivateVolume(options: FSDeactivateOptions, replyHandler reply: @escaping (Error?) -> Void) {
        deactivate(options: options, replyHandler: reply)
    }

    func reclaimItem(_ item: FSItem, replyHandler reply: @escaping (Error?) -> Void) {
        if #available(macOS 27.0, *) {
            _ = item.tryReclaim {}
        }
        if let mtp = item as? MTPItem {
            items.removeValue(forKey: mtp.path)
        }
        reply(nil)
    }

    func lookupItem(named name: FSFileName, inDirectory directory: FSItem, replyHandler reply: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(nil, nil, POSIXError(.ENOENT)); return }
        let path = childPath(dir.path, name.string ?? "")
        if (name.string ?? "").hasPrefix(".") {
            reply(nil, nil, POSIXError(.ENOENT))
            return
        }
        do {
            let attr = try parseAttr(rpc.call(op: 2, payload: cString(path)))
            reply(remember(path: path, attr: attr, parentID: dir.itemID), name, nil)
        } catch {
            reply(nil, nil, error)
        }
    }

    func lookupItem(named name: FSFileName, in directory: FSItem, context: FSContext, replyHandler reply: @escaping (FSLookupItemResult?, (any Error)?) -> Void) {
        lookupItem(named: name, inDirectory: directory) { item, foundName, err in
            if let err {
                reply(nil, err)
                return
            }
            guard let item = item as? MTPItem, let foundName else {
                reply(nil, POSIXError(.ENOENT))
                return
            }
            guard let result = FSLookupItemResult(foundItem: item, itemName: foundName, itemAttributes: self.makeAttributes(item)) else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(result, nil)
        }
    }

    func createItem(named name: FSFileName, type: FSItem.ItemType, inDirectory directory: FSItem, attributes: FSItem.SetAttributesRequest, replyHandler reply: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(nil, nil, POSIXError(.EPERM)); return }
        let path = childPath(dir.path, name.string ?? "")
        if (name.string ?? "").hasPrefix(".") {
            reply(nil, nil, POSIXError(.EPERM))
            return
        }
        let op: UInt16 = type == .directory ? 8 : 9
        do {
            let attr = try parseAttr(rpc.call(op: op, payload: cString(path)))
            reply(remember(path: path, attr: attr, parentID: dir.itemID), name, nil)
        } catch {
            reply(nil, nil, error)
        }
    }

    func createItem(named name: FSFileName, type: FSItem.ItemType, in directory: FSItem, attributes: FSItem.SetAttributesRequest, context: FSContext, replyHandler reply: @escaping (FSCreateItemResult?, (any Error)?) -> Void) {
        createItem(named: name, type: type, inDirectory: directory, attributes: attributes) { item, createdName, err in
            if let err {
                reply(nil, err)
                return
            }
            guard let item = item as? MTPItem, let createdName, let dir = directory as? MTPItem else {
                reply(nil, POSIXError(.EIO))
                return
            }
            guard let result = FSCreateItemResult(newItem: item, newItemName: createdName, newItemAttributes: self.makeAttributes(item), directoryAttributes: self.makeAttributes(dir), freeSpace: nil) else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(result, nil)
        }
    }

    func createSymbolicLink(named name: FSFileName, inDirectory directory: FSItem, attributes: FSItem.SetAttributesRequest, linkContents contents: FSFileName, replyHandler reply: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        reply(nil, nil, POSIXError(.ENOTSUP))
    }

    func createSymbolicLink(named name: FSFileName, in directory: FSItem, attributes: FSItem.SetAttributesRequest, linkContents contents: FSFileName, context: FSContext, replyHandler reply: @escaping (FSCreateSymlinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func createLink(to item: FSItem, named name: FSFileName, inDirectory directory: FSItem, replyHandler reply: @escaping (FSFileName?, Error?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func createLink(to item: FSItem, named name: FSFileName, in directory: FSItem, context: FSContext, replyHandler reply: @escaping (FSCreateLinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func renameItem(_ item: FSItem, inDirectory sourceDirectory: FSItem, named sourceName: FSFileName, to destinationName: FSFileName, inDirectory destinationDirectory: FSItem, overItem: FSItem?, replyHandler reply: @escaping (FSFileName?, Error?) -> Void) {
        guard let srcDir = sourceDirectory as? MTPItem, let dstDir = destinationDirectory as? MTPItem else {
            reply(nil, POSIXError(.ENOENT)); return
        }
        let from = childPath(srcDir.path, sourceName.string ?? "")
        let to = childPath(dstDir.path, destinationName.string ?? "")
        var payload = cString(from)
        payload.append(cString(to))
        do {
            _ = try rpc.call(op: 12, payload: payload)
            if let mtp = item as? MTPItem {
                items.removeValue(forKey: mtp.path)
            }
            reply(destinationName, nil)
        } catch {
            reply(nil, error)
        }
    }

    func renameItem(_ item: FSItem, inDirectory sourceDirectory: FSItem, named sourceName: FSFileName, to destinationName: FSFileName, inDirectory destinationDirectory: FSItem, overItem: FSItem?, context: FSContext, replyHandler reply: @escaping (FSRenameItemResult?, (any Error)?) -> Void) {
        renameItem(item, inDirectory: sourceDirectory, named: sourceName, to: destinationName, inDirectory: destinationDirectory, overItem: overItem) { newName, err in
            if let err {
                reply(nil, err)
                return
            }
            guard let newName, let renamed = item as? MTPItem, let source = sourceDirectory as? MTPItem, let destination = destinationDirectory as? MTPItem else {
                reply(nil, POSIXError(.EIO))
                return
            }
            let over = (overItem as? MTPItem).map { self.makeAttributes($0) }
            guard let result = FSRenameItemResult(newName: newName, renamedItemAttributes: self.makeAttributes(renamed), sourceDirectoryAttributes: self.makeAttributes(source), destinationDirectoryAttributes: self.makeAttributes(destination), overItemAttributes: over, freeSpace: nil) else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(result, nil)
        }
    }

    func removeItem(_ item: FSItem, named name: FSFileName, fromDirectory directory: FSItem, replyHandler reply: @escaping (Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(POSIXError(.ENOENT)); return }
        let path = childPath(dir.path, name.string ?? "")
        let op: UInt16 = (item as? MTPItem)?.directory == true ? 11 : 10
        do {
            _ = try rpc.call(op: op, payload: cString(path))
            items.removeValue(forKey: path)
            reply(nil)
        } catch {
            reply(error)
        }
    }

    func removeItem(_ item: FSItem, named name: FSFileName, from directory: FSItem, context: FSContext, replyHandler reply: @escaping (FSRemoveItemResult?, (any Error)?) -> Void) {
        removeItem(item, named: name, fromDirectory: directory) { err in
            if let err {
                reply(nil, err)
                return
            }
            guard let removed = item as? MTPItem, let dir = directory as? MTPItem else {
                reply(nil, POSIXError(.EIO))
                return
            }
            guard let result = FSRemoveItemResult(itemAttributes: self.makeAttributes(removed), directoryAttributes: self.makeAttributes(dir), freeSpace: nil) else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(result, nil)
        }
    }

    func getAttributes(_ desired: FSItem.GetAttributesRequest, of item: FSItem, replyHandler reply: @escaping (FSItem.Attributes?, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(nil, POSIXError(.ENOENT)); return }
        fsLog.error("getattr \(mtp.path, privacy: .public) id \(mtp.itemID, privacy: .public)")
        reply(makeAttributes(mtp), nil)
    }

    func getAttributes(_ desired: FSItem.GetAttributesRequest, of item: FSItem, context: FSContext, replyHandler reply: @escaping (FSGetAttributesResult?, (any Error)?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(nil, POSIXError(.ENOENT)); return }
        fsLog.error("getattr3 \(mtp.path, privacy: .public) id \(mtp.itemID, privacy: .public)")
        guard let result = FSGetAttributesResult(attributes: makeAttributes(mtp)) else {
            reply(nil, POSIXError(.EIO))
            return
        }
        reply(result, nil)
    }

    func setAttributes(_ request: FSItem.SetAttributesRequest, on item: FSItem, replyHandler reply: @escaping (FSItem.Attributes?, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(nil, POSIXError(.ENOENT)); return }
        if request.isValid(.size), !mtp.directory {
            var payload = cString(mtp.path)
            var size = request.size.littleEndian
            payload.append(Data(bytes: &size, count: 8))
            do {
                let attr = try parseAttr(rpc.call(op: 13, payload: payload))
                mtp.attr = attr
                mtp.itemID = safeIno(attr.ino, path: mtp.path)
                reply(makeAttributes(mtp), nil)
                return
            } catch {
                reply(nil, error)
                return
            }
        }
        reply(makeAttributes(mtp), nil)
    }

    func setAttributes(_ request: FSItem.SetAttributesRequest, on item: FSItem, context: FSContext, replyHandler reply: @escaping (FSSetAttributesResult?, (any Error)?) -> Void) {
        setAttributes(request, on: item) { attrs, err in
            if let err {
                reply(nil, err)
                return
            }
            guard let attrs, let result = FSSetAttributesResult(attributes: attrs, freeSpace: nil) else {
                reply(nil, POSIXError(.EIO))
                return
            }
            reply(result, nil)
        }
    }

    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie, verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?, packer: FSDirectoryEntryPacker, replyHandler reply: @escaping (FSDirectoryVerifier, Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(verifier, POSIXError(.ENOTDIR)); return }
        fsLog.error("readdir \(dir.path, privacy: .public) cookie \(cookie.rawValue, privacy: .public)")
        do {
            let data = try rpc.call(op: 3, payload: cString(dir.path))
            var entries: [(String, Attr, UInt64)] = []
            if attributes == nil {
                let dotDot = dir.path == "/" ? dir.itemID : dir.parentID
                entries.append((".", Attr(isDir: true, ino: dir.itemID, size: 0, mtime: dir.attr.mtime), dir.itemID))
                entries.append(("..", Attr(isDir: true, ino: dotDot, size: 0, mtime: 0), dotDot))
            }
            for parsed in try parseDir(data) {
                let child = remember(path: childPath(dir.path, parsed.0), attr: parsed.1, parentID: dir.itemID)
                entries.append((parsed.0, child.attr, child.itemID))
            }
            // nextCookie is the index of the next entry. 0 is only the initial cookie,
            // so a finished page never restarts the directory.
            let start = cookie == .initial || cookie.rawValue == 0 ? 0 : Int(cookie.rawValue)
            if start < 0 || start > entries.count {
                reply(verifier, NSError(domain: FSKitErrorDomain, code: 4506))
                return
            }
            var index = start
            var packed = 0
            while index < entries.count {
                let entry = entries[index]
                let next = FSDirectoryCookie(rawValue: UInt64(index + 1))
                let item = MTPItem(path: entry.0 == "." || entry.0 == ".." ? dir.path : childPath(dir.path, entry.0), itemID: entry.2, parentID: dir.itemID, directory: entry.1.isDir, attr: entry.1)
                let attrs: FSItem.Attributes? = attributes == nil ? nil : makeAttributes(item)
                let id = fileIdentifier(entry.2, isRoot: dir.path == "/" && (entry.0 == "." || entry.0 == ".."))
                let ok = packer.packEntry(name: FSFileName(string: entry.0), itemType: entry.1.isDir ? .directory : .file, itemID: id, nextCookie: next, attributes: attrs)
                if !ok { break }
                index += 1
                packed += 1
            }
            fsLog.error("readdir \(dir.path, privacy: .public) packed \(packed, privacy: .public) of \(entries.count, privacy: .public)")
            reply(FSDirectoryVerifier(rawValue: 1), nil)
        } catch {
            fsLog.error("readdir fail \(String(describing: error), privacy: .public)")
            reply(verifier, error)
        }
    }

    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie, verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?, packer: FSDirectoryEntryPacker, context: FSContext, replyHandler reply: @escaping (FSEnumerateDirectoryResult?, (any Error)?) -> Void) {
        enumerateDirectory(directory, startingAt: cookie, verifier: verifier, attributes: attributes, packer: packer) { current, err in
            if let err {
                reply(nil, err)
                return
            }
            reply(FSEnumerateDirectoryResult(verifier: current.rawValue), nil)
        }
    }

    func readSymbolicLink(_ item: FSItem, replyHandler reply: @escaping (FSFileName?, Error?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func readSymbolicLink(_ item: FSItem, context: FSContext, replyHandler reply: @escaping (FSReadSymlinkResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    var xattrOperationsInhibited: Bool { false }

    func supportedXattrNames(for item: FSItem) -> [FSFileName] { [] }

    func getXattr(named name: FSFileName, of item: FSItem, replyHandler reply: @escaping (Data?, Error?) -> Void) {
        reply(nil, POSIXError(.ENOATTR))
    }

    func getXattr(named name: FSFileName, of item: FSItem, context: FSContext, replyHandler reply: @escaping (FSGetXattrResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.ENOATTR))
    }

    func setXattr(named name: FSFileName, to value: Data?, on item: FSItem, policy: FSVolume.SetXattrPolicy, replyHandler reply: @escaping (Error?) -> Void) {
        reply(POSIXError(.EPERM))
    }

    func setXattr(named name: FSFileName, to value: Data?, on item: FSItem, policy: FSVolume.SetXattrPolicy, context: FSContext, replyHandler reply: @escaping (FSSetXattrResult?, (any Error)?) -> Void) {
        reply(nil, POSIXError(.EPERM))
    }

    func listXattrs(of item: FSItem, replyHandler reply: @escaping ([FSFileName]?, Error?) -> Void) {
        reply([], nil)
    }

    func listXattrs(of item: FSItem, context: FSContext, replyHandler reply: @escaping (FSListXattrsResult?, (any Error)?) -> Void) {
        guard let result = FSListXattrsResult(xattrNames: []) else {
            reply(nil, POSIXError(.ENOATTR))
            return
        }
        reply(result, nil)
    }

    func openItem(_ item: FSItem, modes: FSVolume.OpenModes, replyHandler reply: @escaping (Error?) -> Void) {
        do {
            try openFile(item, modes: modes)
            reply(nil)
        } catch {
            reply(error)
        }
    }

    func openItem(_ item: FSItem, modes: FSVolume.OpenModes, context: FSContext, replyHandler reply: @escaping (Error?) -> Void) {
        openItem(item, modes: modes, replyHandler: reply)
    }

    private func openFile(_ item: FSItem, modes: FSVolume.OpenModes) throws {
        guard let mtp = item as? MTPItem, !mtp.directory else { return }
        var payload = cString(mtp.path)
        var write: UInt8 = 0
        if modes.contains(.write) {
            write = 1
        }
        payload.append(write)
        _ = try rpc.call(op: 4, payload: payload)
    }

    func closeItem(_ item: FSItem, modes: FSVolume.OpenModes, context: FSContext, replyHandler reply: @escaping (Error?) -> Void) {
        closeItem(item, modes: modes, replyHandler: reply)
    }

    func closeItem(_ item: FSItem, modes: FSVolume.OpenModes, replyHandler reply: @escaping (Error?) -> Void) {
        guard let mtp = item as? MTPItem, !mtp.directory else { reply(nil); return }
        do {
            _ = try rpc.call(op: 5, payload: cString(mtp.path))
            reply(nil)
        } catch {
            reply(error)
        }
    }

    func read(from item: FSItem, at offset: off_t, length: Int, into buffer: FSMutableFileDataBuffer, replyHandler reply: @escaping (Int, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(0, POSIXError(.ENOENT)); return }
        var payload = cString(mtp.path)
        var off = UInt64(offset).littleEndian
        var n = UInt32(length).littleEndian
        payload.append(Data(bytes: &off, count: 8))
        payload.append(Data(bytes: &n, count: 4))
        do {
            let bytes = try rpc.call(op: 6, payload: payload)
            let copied = buffer.withUnsafeMutableBytes { raw -> Int in
                let dst = raw.bindMemory(to: UInt8.self)
                let count = min(dst.count, bytes.count)
                bytes.copyBytes(to: dst, count: count)
                return count
            }
            reply(copied, nil)
        } catch {
            reply(0, error)
        }
    }

    func write(contents: Data, to item: FSItem, at offset: off_t, replyHandler reply: @escaping (Int, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(0, POSIXError(.ENOENT)); return }
        var payload = cString(mtp.path)
        var off = UInt64(offset).littleEndian
        var n = UInt32(contents.count).littleEndian
        payload.append(Data(bytes: &off, count: 8))
        payload.append(Data(bytes: &n, count: 4))
        payload.append(contents)
        do {
            let resp = try rpc.call(op: 7, payload: payload)
            let wrote = resp.count >= 4 ? resp.withUnsafeBytes { Int($0.load(as: UInt32.self)) } : contents.count
            reply(wrote, nil)
        } catch {
            reply(0, error)
        }
    }

    private func safeIno(_ ino: UInt64, path: String) -> UInt64 {
        if path == "/" {
            return FSItem.Identifier.rootDirectory.rawValue
        }
        return ino < 3 ? ino &+ 16 : ino
    }

    private func remember(path: String, attr: Attr, parentID: UInt64) -> MTPItem {
        let id = safeIno(attr.ino, path: path)
        if let existing = items[path] {
            existing.directory = attr.isDir
            existing.attr = attr
            existing.itemID = id
            existing.parentID = parentID
            return existing
        }
        let item = MTPItem(path: path, itemID: id, parentID: parentID, directory: attr.isDir, attr: attr)
        items[path] = item
        return item
    }

    private func makeAttributes(_ item: MTPItem) -> FSItem.Attributes {
        let attr = item.attr
        let a = FSItem.Attributes()
        a.type = attr.isDir ? .directory : .file
        a.mode = attr.isDir ? 0o755 : 0o644
        a.linkCount = attr.isDir ? 2 : 1
        a.uid = getuid()
        a.gid = getgid()
        a.size = attr.isDir ? 0 : attr.size
        a.allocSize = attr.isDir ? 0 : (attr.size + 4095) & ~4095
        a.fileID = fileIdentifier(item.itemID, isRoot: item.path == "/")
        a.parentID = fileIdentifier(item.parentID, isRoot: false)
        a.flags = 0
        a.supportsLimitedXAttrs = false
        let ts = timespec(tv_sec: max(Int(attr.mtime), 0), tv_nsec: 0)
        a.modifyTime = ts
        a.changeTime = ts
        a.accessTime = ts
        a.birthTime = ts
        a.addedTime = ts
        return a
    }

    private func parseDir(_ data: Data) throws -> [(String, Attr)] {
        if data.count < 4 { return [] }
        var out: [(String, Attr)] = []
        try data.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
            let count = Int(raw.load(as: UInt32.self))
            var off = 4
            for _ in 0..<count {
                if off + 2 > raw.count { throw POSIXError(.EIO) }
                let namelen = Int(raw.loadUnaligned(fromByteOffset: off, as: UInt16.self))
                off += 2
                if off + namelen + 25 > raw.count { throw POSIXError(.EIO) }
                let name = String(bytes: raw[off..<off + namelen], encoding: .utf8) ?? ""
                off += namelen
                let isDir = raw.load(fromByteOffset: off, as: UInt8.self) != 0
                let ino = raw.loadUnaligned(fromByteOffset: off + 1, as: UInt64.self)
                let size = raw.loadUnaligned(fromByteOffset: off + 9, as: UInt64.self)
                let mtime = raw.loadUnaligned(fromByteOffset: off + 17, as: Int64.self)
                off += 25
                out.append((name, Attr(isDir: isDir, ino: ino, size: size, mtime: mtime)))
            }
        }
        return out
    }
}

private func sessionFields(_ url: URL) -> [String: String] {
    var fields: [String: String] = [:]
    let scoped = url.startAccessingSecurityScopedResource()
    defer {
        if scoped {
            url.stopAccessingSecurityScopedResource()
        }
    }
    if let text = try? String(contentsOf: url, encoding: .utf8) {
        for line in text.split(separator: "\n") {
            let parts = line.split(separator: "=", maxSplits: 1).map(String.init)
            if parts.count == 2 { fields[parts[0]] = parts[1] }
        }
    }
    // The extension sandbox often cannot read the session file. The file name
    // carries the same port and container id: androidmtp_<port>_<uuid>.session
    let bits = url.deletingPathExtension().lastPathComponent.split(separator: "_", maxSplits: 2).map(String.init)
    if bits.count == 3, bits[0] == "androidmtp" {
        if fields["tcp"] == nil { fields["tcp"] = "127.0.0.1:" + bits[1] }
        if fields["id"] == nil { fields["id"] = bits[2] }
    }
    fsLog.error("session path=\(url.path, privacy: .public) scoped=\(scoped) tcp=\(fields["tcp"] ?? "-", privacy: .public)")
    return fields
}

final class AndroidMTPFS: FSUnaryFileSystem, FSUnaryFileSystemOperations {
    func probeResource(resource: FSResource, replyHandler reply: @escaping (FSProbeResult?, (any Error)?) -> Void) {
        guard let pathResource = resource as? FSPathURLResource else {
            reply(.notRecognized, nil)
            return
        }
        let fields = sessionFields(pathResource.url)
        guard fields["tcp"] != nil || fields["socket"] != nil else {
            reply(.notRecognized, nil)
            return
        }
        let name = fields["volume"] ?? "Android"
        let id = UUID(uuidString: fields["id"] ?? "") ?? UUID()
        reply(.recognized(name: name, containerID: FSContainerIdentifier(uuid: id)), nil)
    }

    func loadResource(resource: FSResource, options: FSTaskOptions, replyHandler reply: @escaping (FSVolume?, (any Error)?) -> Void) {
        guard let pathResource = resource as? FSPathURLResource else {
            fsLog.error("load resource type \(String(describing: type(of: resource)), privacy: .public)")
            reply(nil, POSIXError(.EINVAL))
            return
        }
        let fields = sessionFields(pathResource.url)
        do {
            let rpc: MTPConnection
            if let tcp = fields["tcp"] {
                rpc = try MTPConnection(tcp: tcp)
            } else if let socketPath = fields["socket"] {
                rpc = try MTPConnection(unixPath: socketPath)
            } else {
                reply(nil, POSIXError(.EINVAL))
                return
            }
            let id = UUID(uuidString: fields["id"] ?? "") ?? UUID()
            let volume = MTPVolume(rpc: rpc, name: fields["volume"] ?? "Android", id: id)
            containerStatus = .ready
            reply(volume, nil)
        } catch {
            fsLog.error("load connect failed \(String(describing: error), privacy: .public)")
            reply(nil, error)
        }
    }

    func unloadResource(resource: FSResource, options: FSTaskOptions, replyHandler reply: @escaping ((any Error)?) -> Void) {
        reply(nil)
    }
}

@main
final class AndroidMTPExtension: UnaryFileSystemExtension {
    let fileSystem: AndroidMTPFS
    init() {
        fileSystem = AndroidMTPFS()
    }
}
