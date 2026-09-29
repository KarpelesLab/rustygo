// Program exec starts child processes: itself, and a shell.
//
// This is fork and exec, the wait that collects the child, pipes in both
// directions, the child's environment, and the exit status coming back. The
// most interesting child is this program again, with an environment variable
// telling it which half to be.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// The variable that tells a child what to do, and the values it takes.
const role = "RUSTYGO_EXEC_ROLE"

func main() {
	switch os.Getenv(role) {
	case "":
		parent()
	case "hello":
		fmt.Println("child says hello")
		fmt.Fprintln(os.Stderr, "child says it on stderr too")
	case "fail":
		fmt.Println("child is about to fail")
		os.Exit(3)
	case "echo":
		n, err := io.Copy(os.Stdout, os.Stdin)
		fmt.Fprintf(os.Stderr, "child copied %d bytes, err=%v\n", n, err)
	case "env":
		fmt.Println("child sees", os.Getenv("RUSTYGO_EXEC_NOTE"))
	}
}

func parent() {
	me := os.Args[0]

	// A child that prints on both streams.
	cmd := exec.Command(me)
	cmd.Env = append(os.Environ(), role+"=hello")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	fmt.Println("run:", cmd.Run(), quoted(out.String()), quoted(errb.String()))

	// A child that exits with a status.
	cmd = exec.Command(me)
	cmd.Env = append(os.Environ(), role+"=fail")
	out.Reset()
	cmd.Stdout = &out
	err := cmd.Run()
	fmt.Println("fail:", err, quoted(out.String()))
	var ee *exec.ExitError
	if e, ok := err.(*exec.ExitError); ok {
		ee = e
	}
	fmt.Println("status:", ee != nil, ee.ExitCode(), ee.Exited(), ee.Success())

	// A child reading from a pipe: the parent has to keep writing while the
	// child runs, and the wait has to let it.
	cmd = exec.Command(me)
	cmd.Env = append(os.Environ(), role+"=echo")
	cmd.Stdin = strings.NewReader(strings.Repeat("ping\n", 100))
	errb.Reset()
	cmd.Stderr = &errb
	echoed, err := cmd.Output()
	fmt.Println("echo:", err, len(echoed), strings.Count(string(echoed), "ping"), quoted(errb.String()))

	// The environment the child is given, rather than inherited.
	cmd = exec.Command(me)
	cmd.Env = []string{role + "=env", "RUSTYGO_EXEC_NOTE=a note"}
	noted, err := cmd.Output()
	fmt.Println("env:", err, quoted(string(noted)))

	// A shell, for a child that is not this program.
	sh, err := exec.Command("/bin/sh", "-c", "echo one; echo two 1>&2; exit 0").CombinedOutput()
	fmt.Println("sh:", err, quoted(string(sh)))

	// Something that is not there at all, and something that is not runnable.
	_, err = exec.Command("/nonexistent/rustygo-test").Output()
	fmt.Println("missing:", err != nil, os.IsNotExist(errUnwrap(err)))
	_, err = exec.LookPath("nonexistent-rustygo-test-command")
	fmt.Println("lookpath:", err != nil)

	// A pipe the parent reads as the child writes it.
	cmd = exec.Command(me)
	cmd.Env = append(os.Environ(), role+"=hello")
	pipe, err := cmd.StdoutPipe()
	fmt.Println("pipe:", err, cmd.Start())
	body, err := io.ReadAll(pipe)
	fmt.Println("read:", err, quoted(string(body)), cmd.Wait())
	fmt.Println("done")
}

func quoted(s string) string { return fmt.Sprintf("%q", s) }

func errUnwrap(err error) error {
	var pe *exec.Error
	if e, ok := err.(*exec.Error); ok {
		pe = e
		return pe.Err
	}
	var path *os.PathError
	if e, ok := err.(*os.PathError); ok {
		path = e
		return path.Err
	}
	return err
}
