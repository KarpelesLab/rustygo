// Program filesys works the file layer the way os's own tests do: files,
// directories, permissions, symlinks, renaming, truncation, reading a
// directory, and a deadline on a pipe.
//
// Everything happens inside one temporary directory and nothing prints an
// absolute path, so the output is the same wherever it runs — which is what
// makes it comparable between a gc build and a rustygo one.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func main() {
	dir, err := os.MkdirTemp("", "rustygo-filesys-")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)
	if err := os.Chdir(dir); err != nil {
		fmt.Println("chdir:", err)
		return
	}

	files()
	modes()
	dirs()
	links()
	renameAndTruncate()
	pipeDeadline()
	fmt.Println("done")
}

// files writes a file through every door os offers and reads it back.
func files() {
	fmt.Println("== files ==")
	f, err := os.Create("a.txt")
	fmt.Println("create:", err)
	n, err := f.WriteString("hello, file\n")
	fmt.Println("write:", n, err)
	// A write at an offset, then a read at one, neither moving the other's
	// idea of where it is.
	n, err = f.WriteAt([]byte("HELLO"), 0)
	fmt.Println("writeat:", n, err)
	buf := make([]byte, 5)
	n, err = f.ReadAt(buf, 7)
	fmt.Println("readat:", n, err, string(buf[:n]))
	off, err := f.Seek(0, io.SeekStart)
	fmt.Println("seek:", off, err)
	all, err := io.ReadAll(f)
	fmt.Println("readall:", err, fmt.Sprintf("%q", string(all)))
	fmt.Println("name:", f.Name())
	fmt.Println("sync:", f.Sync())
	fmt.Println("close:", f.Close())
	fmt.Println("close again:", f.Close() != nil)

	body, err := os.ReadFile("a.txt")
	fmt.Println("readfile:", err, len(body))
	fmt.Println("writefile:", os.WriteFile("b.txt", []byte("b"), 0o600))

	st, err := os.Stat("a.txt")
	fmt.Println("stat:", err, st.Name(), st.Size(), st.IsDir(), st.Mode().IsRegular())

	// What is not there, said the way every caller tests for it.
	_, err = os.Open("nope.txt")
	fmt.Println("missing:", err != nil, os.IsNotExist(err), errors.Is(err, fs.ErrNotExist))
	var pe *fs.PathError
	fmt.Println("patherror:", errors.As(err, &pe), pe.Op, pe.Path)

	// Appending, and the exclusive create that refuses an existing file.
	f, err = os.OpenFile("a.txt", os.O_WRONLY|os.O_APPEND, 0)
	fmt.Println("append open:", err)
	_, err = f.WriteString("more\n")
	fmt.Println("append write:", err, f.Close())
	body, _ = os.ReadFile("a.txt")
	fmt.Println("appended:", fmt.Sprintf("%q", string(body)))
	_, err = os.OpenFile("a.txt", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	fmt.Println("excl:", err != nil, os.IsExist(err))
}

// modes checks the permission bits a file is created with and chmod changes.
func modes() {
	fmt.Println("== modes ==")
	fmt.Println("writefile:", os.WriteFile("perm.txt", nil, 0o640))
	st, err := os.Stat("perm.txt")
	fmt.Println("mode:", err, st.Mode(), st.Mode().Perm())
	fmt.Println("chmod:", os.Chmod("perm.txt", 0o444))
	st, _ = os.Stat("perm.txt")
	fmt.Println("mode now:", st.Mode())
	// A file nobody may write is a file this process cannot open for writing,
	// unless it is root — which the differential harness is not.
	_, err = os.OpenFile("perm.txt", os.O_WRONLY, 0)
	fmt.Println("readonly:", err != nil, os.IsPermission(err))
	fmt.Println("chmod back:", os.Chmod("perm.txt", 0o644))
}

// dirs makes a tree, reads it, and takes it apart.
func dirs() {
	fmt.Println("== dirs ==")
	fmt.Println("mkdir:", os.Mkdir("d", 0o755))
	fmt.Println("mkdir again:", os.Mkdir("d", 0o755) != nil)
	fmt.Println("mkdirall:", os.MkdirAll("d/e/f", 0o755))
	for _, name := range []string{"d/one", "d/two", "d/e/three"} {
		if err := os.WriteFile(name, []byte(name), 0o644); err != nil {
			fmt.Println("write:", err)
		}
	}
	entries, err := os.ReadDir("d")
	fmt.Println("readdir:", err, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		fmt.Println("  ", e.Name(), e.IsDir(), e.Type().IsDir(), err == nil && info.Name() == e.Name())
	}
	// Readdirnames hands back whatever order the file system has, so it is
	// sorted before printing; os.ReadDir sorts for its caller already.
	f, err := os.Open("d")
	fmt.Println("open dir:", err)
	names, err := f.Readdirnames(-1)
	sort.Strings(names)
	fmt.Println("names:", err, names, f.Close())
	// Reading a directory in batches, as Readdir's contract allows.
	f, _ = os.Open("d")
	first, err := f.Readdir(2)
	fmt.Println("readdir 2:", err, len(first))
	rest, err := f.Readdir(-1)
	fmt.Println("readdir rest:", err, len(rest), f.Close())
	// A directory is not a file to read bytes from.
	f, _ = os.Open("d")
	_, err = io.ReadAll(f)
	fmt.Println("read dir bytes:", err != nil, f.Close())

	var walked []string
	err = filepath.WalkDir("d", func(p string, d fs.DirEntry, err error) error {
		walked = append(walked, p)
		return err
	})
	sort.Strings(walked)
	fmt.Println("walk:", err, walked)

	fmt.Println("remove nonempty:", os.Remove("d") != nil)
	fmt.Println("removeall:", os.RemoveAll("d"))
	fmt.Println("removeall again:", os.RemoveAll("d"))
	_, err = os.Stat("d")
	fmt.Println("gone:", os.IsNotExist(err))
}

// links makes a symlink and a hard link, and tells Stat from Lstat.
func links() {
	fmt.Println("== links ==")
	fmt.Println("writefile:", os.WriteFile("target.txt", []byte("target"), 0o644))
	fmt.Println("symlink:", os.Symlink("target.txt", "link.txt"))
	dest, err := os.Readlink("link.txt")
	fmt.Println("readlink:", err, dest)
	st, err := os.Stat("link.txt")
	fmt.Println("stat:", err, st.Size(), st.Mode()&fs.ModeSymlink != 0)
	lst, err := os.Lstat("link.txt")
	fmt.Println("lstat:", err, lst.Mode()&fs.ModeSymlink != 0)
	body, err := os.ReadFile("link.txt")
	fmt.Println("through link:", err, string(body))
	fmt.Println("link:", os.Link("target.txt", "hard.txt"))
	hst, err := os.Stat("hard.txt")
	fmt.Println("hard:", err, hst.Size(), os.SameFile(st, hst))
	// A symlink to nothing: Lstat sees the link, Stat follows it and fails.
	fmt.Println("dangling:", os.Symlink("missing.txt", "dangling.txt"))
	_, err = os.Lstat("dangling.txt")
	fmt.Println("lstat dangling:", err)
	_, err = os.Stat("dangling.txt")
	fmt.Println("stat dangling:", os.IsNotExist(err))
	fmt.Println("readlink of a file:", os.Remove("dangling.txt"))
	_, err = os.Readlink("target.txt")
	fmt.Println("not a link:", err != nil)
}

// renameAndTruncate moves a file and changes its length in both directions.
func renameAndTruncate() {
	fmt.Println("== rename and truncate ==")
	fmt.Println("writefile:", os.WriteFile("from.txt", []byte("0123456789"), 0o644))
	fmt.Println("rename:", os.Rename("from.txt", "to.txt"))
	_, err := os.Stat("from.txt")
	fmt.Println("old gone:", os.IsNotExist(err))
	fmt.Println("truncate:", os.Truncate("to.txt", 4))
	body, err := os.ReadFile("to.txt")
	fmt.Println("short:", err, fmt.Sprintf("%q", string(body)))
	// Growing a file pads it with zeros.
	fmt.Println("grow:", os.Truncate("to.txt", 6))
	body, _ = os.ReadFile("to.txt")
	fmt.Println("grown:", fmt.Sprintf("%q", string(body)))
	f, err := os.OpenFile("to.txt", os.O_RDWR, 0)
	fmt.Println("open:", err)
	fmt.Println("f.Truncate:", f.Truncate(2))
	st, err := f.Stat()
	fmt.Println("size:", err, st.Size(), f.Close())
	fmt.Println("truncate missing:", os.Truncate("missing.txt", 0) != nil)
	fmt.Println("rename missing:", os.Rename("missing.txt", "x.txt") != nil)
}

// pipeDeadline reads a pipe that nobody writes, with a deadline on it, which
// is the netpoller's promise: the read comes back, it does not hang.
func pipeDeadline() {
	fmt.Println("== pipe deadline ==")
	r, w, err := os.Pipe()
	fmt.Println("pipe:", err)
	fmt.Println("deadline:", r.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	buf := make([]byte, 8)
	_, err = r.Read(buf)
	fmt.Println("read:", errors.Is(err, os.ErrDeadlineExceeded))
	// A deadline already past refuses at once.
	fmt.Println("deadline past:", r.SetReadDeadline(time.Now().Add(-time.Hour)))
	_, err = r.Read(buf)
	fmt.Println("read again:", errors.Is(err, os.ErrDeadlineExceeded))
	// Clearing it, and a write the reader then sees.
	fmt.Println("clear:", r.SetReadDeadline(time.Time{}))
	n, err := w.WriteString("ping")
	fmt.Println("write:", n, err)
	n, err = r.Read(buf)
	fmt.Println("read:", n, err, string(buf[:n]))
	// A closed writer is an EOF for the reader.
	fmt.Println("close w:", w.Close())
	_, err = r.Read(buf)
	fmt.Println("eof:", err == io.EOF)
	fmt.Println("close r:", r.Close())
}
