import Darwin
import ExtensionFoundation
import Foundation
import FSKit

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
        let ino = raw.load(fromByteOffset: 1, as: UInt64.self)
        let size = raw.load(fromByteOffset: 9, as: UInt64.self)
        let mtime = raw.load(fromByteOffset: 17, as: Int64.self)
        return Attr(isDir: dir, ino: ino, size: size, mtime: mtime)
    }
}

private final class MTPItem: FSItem {
    let path: String
    let itemID: UInt64
    var directory: Bool
    init(path: String, itemID: UInt64, directory: Bool) {
        self.path = path
        self.itemID = itemID
        self.directory = directory
        super.init()
    }
}

private func childPath(_ dir: String, _ name: String) -> String {
    if dir == "/" { return "/" + name }
    return dir + "/" + name
}

private final class MTPVolume: FSVolume, FSVolume.Operations, FSVolume.OpenCloseOperations, FSVolume.ReadWriteOperations {
    let rpc: MTPConnection
    private var items: [String: MTPItem] = [:]

    init(rpc: MTPConnection, name: String, id: UUID) {
        self.rpc = rpc
        super.init(volumeID: FSVolume.Identifier(uuid: id), volumeName: FSFileName(string: name))
    }

    var supportedVolumeCapabilities: FSVolume.SupportedCapabilities {
        let caps = FSVolume.SupportedCapabilities()
        caps.supports2TBFiles = true
        caps.supports64BitObjectIDs = true
        caps.doesNotSupportSettingFilePermissions = true
        caps.caseFormat = .sensitive
        return caps
    }

    var volumeStatistics: FSStatFSResult {
        let stat = FSStatFSResult(fileSystemTypeName: "androidmtp")
        stat.blockSize = 4096
        if let data = try? rpc.call(op: 1), data.count >= 16 {
            let total = data.withUnsafeBytes { $0.load(as: UInt64.self) }
            let free = data.withUnsafeBytes { $0.load(fromByteOffset: 8, as: UInt64.self) }
            stat.totalBytes = total
            stat.availableBytes = free
            stat.freeBytes = free
        }
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
            let attr = try parseAttr(rpc.call(op: 2, payload: cString("/")))
            let root = remember(path: "/", attr: attr)
            reply(root, nil)
        } catch {
            reply(nil, error)
        }
    }

    func deactivate(options: FSDeactivateOptions, replyHandler reply: @escaping (Error?) -> Void) {
        items.removeAll()
        reply(nil)
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
        do {
            let attr = try parseAttr(rpc.call(op: 2, payload: cString(path)))
            reply(remember(path: path, attr: attr), name, nil)
        } catch {
            reply(nil, nil, error)
        }
    }

    func createItem(named name: FSFileName, type: FSItem.ItemType, inDirectory directory: FSItem, attributes: FSItem.SetAttributesRequest, replyHandler reply: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(nil, nil, POSIXError(.EPERM)); return }
        let path = childPath(dir.path, name.string ?? "")
        let op: UInt16 = type == .directory ? 8 : 9
        do {
            let attr = try parseAttr(rpc.call(op: op, payload: cString(path)))
            reply(remember(path: path, attr: attr), name, nil)
        } catch {
            reply(nil, nil, error)
        }
    }

    func createSymbolicLink(named name: FSFileName, inDirectory directory: FSItem, attributes: FSItem.SetAttributesRequest, linkContents contents: FSFileName, replyHandler reply: @escaping (FSItem?, FSFileName?, Error?) -> Void) {
        reply(nil, nil, POSIXError(.ENOTSUP))
    }

    func createLink(to item: FSItem, named name: FSFileName, inDirectory directory: FSItem, replyHandler reply: @escaping (FSFileName?, Error?) -> Void) {
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

    func getAttributes(_ desired: FSItem.GetAttributesRequest, of item: FSItem, replyHandler reply: @escaping (FSItem.Attributes?, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(nil, POSIXError(.ENOENT)); return }
        do {
            let attr = try parseAttr(rpc.call(op: 2, payload: cString(mtp.path)))
            reply(makeAttributes(attr), nil)
        } catch {
            reply(nil, error)
        }
    }

    func setAttributes(_ request: FSItem.SetAttributesRequest, on item: FSItem, replyHandler reply: @escaping (FSItem.Attributes?, Error?) -> Void) {
        guard let mtp = item as? MTPItem else { reply(nil, POSIXError(.ENOENT)); return }
        if request.isValid(.size) {
            var payload = cString(mtp.path)
            var size = request.size.littleEndian
            payload.append(Data(bytes: &size, count: 8))
            do {
                let attr = try parseAttr(rpc.call(op: 13, payload: payload))
                reply(makeAttributes(attr), nil)
                return
            } catch {
                reply(nil, error)
                return
            }
        }
        do {
            let attr = try parseAttr(rpc.call(op: 2, payload: cString(mtp.path)))
            reply(makeAttributes(attr), nil)
        } catch {
            reply(nil, error)
        }
    }

    func enumerateDirectory(_ directory: FSItem, startingAt cookie: FSDirectoryCookie, verifier: FSDirectoryVerifier, attributes: FSItem.GetAttributesRequest?, packer: FSDirectoryEntryPacker, replyHandler reply: @escaping (FSDirectoryVerifier, Error?) -> Void) {
        guard let dir = directory as? MTPItem else { reply(verifier, POSIXError(.ENOTDIR)); return }
        do {
            let data = try rpc.call(op: 3, payload: cString(dir.path))
            var entries: [(String, Attr)] = []
            if attributes == nil {
                entries.append((".", Attr(isDir: true, ino: dir.itemID, size: 0, mtime: 0)))
                entries.append(("..", Attr(isDir: true, ino: FSItem.Identifier.rootDirectory.rawValue, size: 0, mtime: 0)))
            }
            entries.append(contentsOf: try parseDir(data))
            var index = cookie == .initial ? 0 : Int(cookie.rawValue)
            if index < 0 || index > entries.count {
                reply(verifier, POSIXError(.EINVAL))
                return
            }
            while index < entries.count {
                let entry = entries[index]
                let next = FSDirectoryCookie(rawValue: UInt64(index + 1))
                let attrs: FSItem.Attributes? = attributes == nil ? nil : makeAttributes(entry.1)
                let ok = packer.packEntry(name: FSFileName(string: entry.0), itemType: entry.1.isDir ? .directory : .file, itemID: FSItem.Identifier(rawValue: entry.1.ino) ?? .rootDirectory, nextCookie: next, attributes: attrs)
                index += 1
                if !ok { break }
            }
            reply(FSDirectoryVerifier(rawValue: 1), nil)
        } catch {
            reply(verifier, error)
        }
    }

    func readSymbolicLink(_ item: FSItem, replyHandler reply: @escaping (FSFileName?, Error?) -> Void) {
        reply(nil, POSIXError(.ENOTSUP))
    }

    func openItem(_ item: FSItem, modes: FSVolume.OpenModes, replyHandler reply: @escaping (Error?) -> Void) {
        guard let mtp = item as? MTPItem, !mtp.directory else { reply(nil); return }
        var payload = cString(mtp.path)
        let write: UInt8 = 1
        payload.append(write)
        do {
            _ = try rpc.call(op: 4, payload: payload)
            reply(nil)
        } catch {
            reply(error)
        }
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

    private func remember(path: String, attr: Attr) -> MTPItem {
        if let existing = items[path] {
            existing.directory = attr.isDir
            return existing
        }
        let item = MTPItem(path: path, itemID: attr.ino, directory: attr.isDir)
        items[path] = item
        return item
    }

    private func makeAttributes(_ attr: Attr) -> FSItem.Attributes {
        let a = FSItem.Attributes()
        a.type = attr.isDir ? .directory : .file
        a.mode = attr.isDir ? 0o755 : 0o644
        a.linkCount = attr.isDir ? 2 : 1
        a.uid = getuid()
        a.gid = getgid()
        a.size = attr.size
        a.allocSize = (attr.size + 4095) & ~4095
        a.fileID = FSItem.Identifier(rawValue: attr.ino) ?? .rootDirectory
        let ts = timespec(tv_sec: Int(attr.mtime), tv_nsec: 0)
        a.modifyTime = ts
        a.changeTime = ts
        a.accessTime = ts
        a.birthTime = ts
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
                let namelen = Int(raw.load(fromByteOffset: off, as: UInt16.self))
                off += 2
                if off + namelen + 25 > raw.count { throw POSIXError(.EIO) }
                let name = String(bytes: raw[off..<off + namelen], encoding: .utf8) ?? ""
                off += namelen
                let isDir = raw.load(fromByteOffset: off, as: UInt8.self) != 0
                let ino = raw.load(fromByteOffset: off + 1, as: UInt64.self)
                let size = raw.load(fromByteOffset: off + 9, as: UInt64.self)
                let mtime = raw.load(fromByteOffset: off + 17, as: Int64.self)
                off += 25
                out.append((name, Attr(isDir: isDir, ino: ino, size: size, mtime: mtime)))
            }
        }
        return out
    }
}

private func sessionFields(_ url: URL) -> [String: String] {
    _ = url.startAccessingSecurityScopedResource()
    defer { url.stopAccessingSecurityScopedResource() }
    guard let text = try? String(contentsOf: url, encoding: .utf8) else { return [:] }
    var fields: [String: String] = [:]
    for line in text.split(separator: "\n") {
        let parts = line.split(separator: "=", maxSplits: 1).map(String.init)
        if parts.count == 2 { fields[parts[0]] = parts[1] }
    }
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
