#!/usr/bin/env python3
"""Pure Python policy tests for the fixed audit ownership fixture provisioner."""

from __future__ import annotations

import errno
import importlib.util
import os
import stat
import unittest
from pathlib import Path
from types import SimpleNamespace

SCRIPT = Path(__file__).with_name("provision_audit_ownership_fixtures.py")
spec = importlib.util.spec_from_file_location("ownership_fixture_provisioner", SCRIPT)
provisioner = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(provisioner)

TEST_FLAGS = provisioner.OpenFlags(
    read_only=0,
    write_only=1,
    directory=2,
    cloexec=4,
    nofollow=8,
    create=16,
    exclusive=32,
)


class Node:
    def __init__(self, name, kind, uid, gid, mode, parent=None):
        self.name = name
        self.kind = kind
        self.uid = uid
        self.gid = gid
        self.mode = mode
        self.parent = parent
        self.children = {}
        self.xattrs = []
        self.size = 0
        self.nlink = 1 if kind == "file" else 2 if kind == "directory" else 1
        self.inode = id(self)

    def path(self):
        if self.parent is None:
            return "/"
        parts = [self.name]
        current = self.parent
        while current.parent is not None:
            parts.append(current.name)
            current = current.parent
        return "/" + "/".join(reversed(parts))


class MemoryOperations:
    """Small dirfd-only filesystem model; it never invokes host privilege APIs."""

    def __init__(self):
        self.root = Node("", "directory", 0, 0, 0o755)
        self.fds = {}
        self.next_fd = 10
        self.events = []
        self._seed_dir(self.root, "var", 0, 0, 0o755)
        self._seed_dir(self.root.children["var"], "lib", 0, 0, 0o755)

    def _seed_dir(self, parent, name, uid, gid, mode):
        node = Node(name, "directory", uid, gid, mode, parent)
        parent.children[name] = node
        return node

    def _seed_file(self, parent, name, uid=0, gid=0, mode=0o600, size=0, nlink=1):
        node = Node(name, "file", uid, gid, mode, parent)
        node.size = size
        node.nlink = nlink
        parent.children[name] = node
        return node

    def _fd(self, node):
        fd = self.next_fd
        self.next_fd += 1
        self.fds[fd] = node
        return fd

    def open(self, name, flags, mode=0o777, *, dir_fd=None):
        self.events.append(("open", name, dir_fd, flags))
        if name == "/" and dir_fd is None:
            return self._fd(self.root)
        if dir_fd not in self.fds:
            raise OSError(errno.EBADF, "invalid directory descriptor")
        parent = self.fds[dir_fd]
        if parent.kind != "directory":
            raise NotADirectoryError(name)
        node = parent.children.get(name)
        if flags & TEST_FLAGS.create:
            if node is not None and flags & TEST_FLAGS.exclusive:
                raise FileExistsError(errno.EEXIST, "entry already exists", name)
            if node is None:
                node = self._seed_file(parent, name, 0, 0, mode & 0o7777)
        elif node is None:
            raise FileNotFoundError(errno.ENOENT, "entry not found", name)
        if node.kind == "symlink":
            raise OSError(errno.ELOOP, "symbolic link rejected", name)
        if flags & TEST_FLAGS.directory and node.kind != "directory":
            raise NotADirectoryError(errno.ENOTDIR, "not a directory", name)
        return self._fd(node)

    def mkdir(self, name, mode=0o777, *, dir_fd=None):
        self.events.append(("mkdir", name, dir_fd))
        if dir_fd not in self.fds or self.fds[dir_fd].kind != "directory":
            raise OSError(errno.EBADF, "invalid directory descriptor")
        parent = self.fds[dir_fd]
        if name in parent.children:
            raise FileExistsError(errno.EEXIST, "entry already exists", name)
        self._seed_dir(parent, name, 0, 0, mode & 0o7777)

    def fstat(self, fd):
        self.events.append(("fstat", fd))
        node = self.fds[fd]
        kind = {
            "directory": stat.S_IFDIR,
            "file": stat.S_IFREG,
            "symlink": stat.S_IFLNK,
        }[node.kind]
        return SimpleNamespace(
            st_mode=kind | node.mode,
            st_uid=node.uid,
            st_gid=node.gid,
            st_nlink=node.nlink,
            st_size=node.size,
            st_ino=node.inode,
            st_dev=1,
        )

    def fchmod(self, fd, mode):
        node = self.fds[fd]
        self.events.append(("fchmod", node.path(), mode))
        node.mode = mode & 0o7777

    def fchown(self, fd, uid, gid):
        node = self.fds[fd]
        self.events.append(("fchown", node.path(), uid, gid))
        node.uid, node.gid = uid, gid

    def listxattr(self, fd):
        self.events.append(("listxattr", fd))
        return list(self.fds[fd].xattrs)

    def close(self, fd):
        self.events.append(("close", fd))
        self.fds.pop(fd, None)

    def node(self, path):
        current = self.root
        for part in path.strip("/").split("/"):
            if not part:
                continue
            current = current.children[part]
        return current

    def seed_dir_path(self, path, *, uid=0, gid=0, mode=0o755):
        current = self.root
        for part in path.strip("/").split("/"):
            if part not in current.children:
                current = self._seed_dir(current, part, uid, gid, mode)
            else:
                current = current.children[part]
        return current

    def seed_symlink(self, parent_path, name):
        parent = self.node(parent_path)
        node = Node(name, "symlink", 0, 0, 0o777, parent)
        parent.children[name] = node
        return node


class OwnershipFixtureProvisionerTests(unittest.TestCase):
    def test_creates_only_fixed_empty_entries_with_exact_metadata(self):
        ops = MemoryOperations()
        original_var = ops.node("/var")
        original_lib = ops.node("/var/lib")
        root_fd = provisioner.open_fixture_root(ops=ops, trusted_uid=0, flags=TEST_FLAGS)
        try:
            result = provisioner.provision_fixtures(root_fd, 1200, 1200, ops=ops, trusted_uid=0, flags=TEST_FLAGS)
        finally:
            ops.close(root_fd)

        self.assertEqual(result["status"], "created")
        self.assertEqual(result["root"], f"{provisioner.FIXTURE_ROOT}/1200")
        self.assertEqual(set(ops.node("/var/lib/tbound/audit-ownership-fixtures/1200").children), {"foreign-dir", "file-parent"})
        expected = {
            "/var/lib/tbound/audit-ownership-fixtures/1200": ("directory", 1200, 1200, 0o700),
            "/var/lib/tbound/audit-ownership-fixtures/1200/foreign-dir": ("directory", 65534, 65534, 0o755),
            "/var/lib/tbound/audit-ownership-fixtures/1200/file-parent": ("directory", 1200, 1200, 0o700),
            "/var/lib/tbound/audit-ownership-fixtures/1200/file-parent/journal.jsonl": ("file", 65534, 1200, 0o660),
            "/var/lib/tbound/audit-ownership-fixtures/1200/file-parent/owned-insecure-mode.jsonl": ("file", 1200, 1200, 0o660),
        }
        for path, (kind, uid, gid, mode) in expected.items():
            node = ops.node(path)
            self.assertEqual((node.kind, node.uid, node.gid, node.mode), (kind, uid, gid, mode), path)
            self.assertEqual(node.xattrs, [], path)
            if kind == "file":
                self.assertEqual((node.size, node.nlink), (0, 1), path)
        self.assertIs(ops.node("/var"), original_var)
        self.assertIs(ops.node("/var/lib"), original_lib)
        self.assertEqual((original_var.uid, original_var.gid, original_var.mode), (0, 0, 0o755))
        self.assertEqual((original_lib.uid, original_lib.gid, original_lib.mode), (0, 0, 0o755))
        self.assertFalse(any(event[0] in {"unlink", "rmdir", "exec", "write"} for event in ops.events))
        for event in ops.events:
            if event[0] == "open" and event[1] != "/":
                self.assertIsNotNone(event[2], "all child opens must be descriptor-relative")
                self.assertTrue(event[3] & TEST_FLAGS.nofollow, "all child opens must reject links")

    def test_rejects_symlinked_trusted_ancestor_without_following_it(self):
        ops = MemoryOperations()
        elsewhere = ops.seed_dir_path("/var/lib/elsewhere")
        ops._seed_file(elsewhere, "marker")
        ops.seed_symlink("/var/lib", "tbound")
        with self.assertRaises(provisioner.ProvisionError):
            provisioner.open_fixture_root(ops=ops, trusted_uid=0, flags=TEST_FLAGS)
        self.assertIn("marker", ops.node("/var/lib/elsewhere").children)
        self.assertNotIn("audit-ownership-fixtures", ops.node("/var/lib/elsewhere").children)
        self.assertTrue(any(event[0] == "open" and event[1] == "tbound" and event[3] & TEST_FLAGS.nofollow for event in ops.events))

    def test_rejects_existing_unsafe_base_xattrs_and_modes(self):
        for unsafe in ("xattr", "mode"):
            with self.subTest(unsafe=unsafe):
                ops = MemoryOperations()
                node = ops.seed_dir_path("/var/lib/tbound/audit-ownership-fixtures")
                if unsafe == "xattr":
                    node.xattrs.append("system.posix_acl_default")
                else:
                    node.mode = 0o775
                with self.assertRaises(provisioner.ProvisionError):
                    provisioner.open_fixture_root(ops=ops, trusted_uid=0, flags=TEST_FLAGS)
                self.assertEqual(node.children, {})
                self.assertEqual(node.mode, 0o775 if unsafe == "mode" else 0o755)

    def test_rejects_noncanonical_fixture_base_before_creating_uid_tree(self):
        for label, gid, mode in (("wrong-gid", 1, 0o755), ("private-mode", 0, 0o700)):
            with self.subTest(base=label):
                ops = MemoryOperations()
                base = ops.seed_dir_path("/var/lib/tbound/audit-ownership-fixtures", gid=gid, mode=mode)
                root_fd = provisioner.open_fixture_root(ops=ops, trusted_uid=0, flags=TEST_FLAGS)
                try:
                    with self.assertRaises(provisioner.ProvisionError):
                        provisioner.provision_fixtures(root_fd, 1200, 1200, ops=ops, trusted_uid=0, flags=TEST_FLAGS)
                finally:
                    ops.close(root_fd)
                self.assertEqual(base.children, {})
                self.assertEqual((base.uid, base.gid, base.mode), (0, gid, mode))
                self.assertFalse(any(event[0] in {"mkdir", "fchown", "fchmod", "unlink", "rmdir"} and event[1] != "tbound" for event in ops.events))
    def test_refuses_any_preexisting_uid_tree_without_mutating_it(self):
        ops = MemoryOperations()
        base = ops.seed_dir_path("/var/lib/tbound/audit-ownership-fixtures")
        existing = ops._seed_dir(base, "1200", 0, 0, 0o700)
        sentinel = ops._seed_file(existing, "operator-data", size=7)
        root_fd = provisioner.open_fixture_root(ops=ops, trusted_uid=0, flags=TEST_FLAGS)
        try:
            with self.assertRaises(FileExistsError):
                provisioner.provision_fixtures(root_fd, 1200, 1200, ops=ops, trusted_uid=0, flags=TEST_FLAGS)
        finally:
            ops.close(root_fd)
        self.assertIs(ops.node("/var/lib/tbound/audit-ownership-fixtures/1200/operator-data"), sentinel)
        self.assertEqual((existing.uid, existing.gid, existing.mode), (0, 0, 0o700))
        self.assertEqual((sentinel.uid, sentinel.gid, sentinel.mode, sentinel.size), (0, 0, 0o600, 7))
        self.assertFalse(any(event[0] in {"fchown", "fchmod", "unlink", "rmdir"} for event in ops.events))

    def test_rejects_invalid_trial_identity_and_all_cli_arguments(self):
        for uid, gid in ((0, 1000), (999, 1000), (65534, 1000), (1200, 0), (1200, 65534)):
            with self.subTest(uid=uid, gid=gid), self.assertRaises(provisioner.ProvisionError):
                provisioner.validate_trial_identity(uid, gid)
        self.assertEqual(provisioner.main(["--root", "/tmp"]), 2)


if __name__ == "__main__":
    unittest.main()