# r9 read-only xattr census. Derived from the R7 xattr-list.py (reviewed in the R7 archive).
# Changes from R7, all in service of a complete and honest enumeration:
#   - scandir/lstat/listxattr/getxattr failures are recorded with errno, never silently skipped
#     (R7 used os.walk with no onerror, which drops unreadable directories without a trace)
#   - directories on a different st_dev than the root are pruned and recorded (no cross-mount descent)
#   - symlinks are never followed; only the target string length and sha256 are recorded
#   - values are reported as [length, sha256] only; file content is never opened
# Run as: python3 -I xattr-census-r9.py <absolute-root-dir>   (stdout = JSON lines, one per entry + identity + summary)
import hashlib, json, os, stat, sys

def identity():
    out = {}
    with open('/proc/self/status') as f:
        for line in f:
            k, _, v = line.partition(':')
            if k in ('Uid', 'Gid', 'Groups', 'CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb', 'NoNewPrivs'):
                out[k] = v.strip()
    for m in ('uid_map', 'gid_map'):
        with open('/proc/self/' + m) as f:
            out[m] = [' '.join(line.split()) for line in f]
    with open('/proc/self/setgroups') as f:
        out['setgroups'] = f.read().strip()
    out['user_ns'] = os.readlink('/proc/self/ns/user')
    return out

def main():
    arg = sys.argv[1]
    if os.path.islink(arg) or not os.path.isdir(arg):
        sys.exit('root must be a real directory, not a symlink: ' + arg)
    root = os.path.realpath(arg)
    rst = os.lstat(root)
    root_dev = rst.st_dev
    counts = {'dir': 0, 'file': 0, 'symlink': 0, 'other': 0}
    ns_names = {}
    errs = {'scandir': [], 'lstat': [], 'listxattr': 0, 'getxattr': 0, 'readlink': 0}
    cross = []
    n = [0]

    def rel(p):
        return '.' if p == root else os.path.relpath(p, root)

    def emit(obj):
        sys.stdout.write(json.dumps(obj, sort_keys=True) + '\n')

    def entry(p, st):
        if stat.S_ISDIR(st.st_mode):
            kind = 'dir'
        elif stat.S_ISREG(st.st_mode):
            kind = 'file'
        elif stat.S_ISLNK(st.st_mode):
            kind = 'symlink'
        else:
            kind = 'other'
        counts[kind] += 1
        n[0] += 1
        rec = {'path': rel(p), 'kind': kind, 'dev': st.st_dev, 'ino': st.st_ino,
               'mode': oct(st.st_mode & 0o7777), 'uid': st.st_uid, 'gid': st.st_gid}
        if kind == 'symlink':
            try:
                t = os.fsencode(os.readlink(p))
                rec['link_target_len'] = len(t)
                rec['link_target_sha256'] = hashlib.sha256(t).hexdigest()
                rec['link_target_absolute'] = t.startswith(b'/')
            except OSError as e:
                rec['readlink_errno'] = e.errno
                errs['readlink'] += 1
        try:
            names = sorted(os.listxattr(p, follow_symlinks=False))
        except OSError as e:
            rec['listxattr_errno'] = e.errno
            errs['listxattr'] += 1
            names = []
        xs = {}
        for name in names:
            ns = name.split('.', 1)[0]
            ns_names[ns] = ns_names.get(ns, 0) + 1
            try:
                v = os.getxattr(p, name, follow_symlinks=False)
                xs[name] = [len(v), hashlib.sha256(v).hexdigest()]
            except OSError as e:
                xs[name] = {'getxattr_errno': e.errno}
                errs['getxattr'] += 1
        if xs:
            rec['xattrs'] = xs
        return rec

    emit({'record': 'identity', 'identity': identity()})
    emit({'record': 'root', 'root': root, 'root_dev': root_dev, 'root_ino': rst.st_ino,
          'root_mode': oct(rst.st_mode & 0o7777), 'root_uid': rst.st_uid, 'root_gid': rst.st_gid})
    emit(entry(root, rst))
    stack = [root]
    while stack:
        d = stack.pop()
        try:
            with os.scandir(d) as it:
                children = sorted(e.path for e in it)
        except OSError as e:
            errs['scandir'].append({'path': rel(d), 'errno': e.errno})
            continue
        for p in children:
            try:
                st = os.lstat(p)
            except OSError as e:
                errs['lstat'].append({'path': rel(p), 'errno': e.errno})
                continue
            if stat.S_ISDIR(st.st_mode) and st.st_dev != root_dev:
                cross.append({'path': rel(p), 'dev': st.st_dev})
                continue
            emit(entry(p, st))
            if stat.S_ISDIR(st.st_mode):
                stack.append(p)
    emit({'record': 'summary', 'root': root, 'entries': n[0], 'counts': counts,
          'xattr_names_by_namespace': ns_names, 'errors': errs, 'cross_device_pruned': cross})

if __name__ == '__main__':
    main()
