package fsutil

import (
	"bytes"
	. "github.com/wrouesnel/docker-vde-plugin/logutil"
	"github.com/kardianos/osext"
	"github.com/wrouesnel/go.log"
	"io"
	"os"
	"os/exec"
)

// If the program dies, we need to shutdown gracefully. This channel is
// closed by the signal handler when we need to do a panic and die in the
// main go-routine (which triggers our deferred handlers).
// Every function here should react to this sensibly.
var interruptCh = make(chan interface{})

// Calling this closes the interrupt channel, causes all executing processes to
// die (with a panic). This let's deferred clean up handlers run in the owning
// goroutine.
func Interrupt() {
	if interruptCh != nil {
		close(interruptCh)
		interruptCh = nil
	}
}

// Exit Panicly if paths do not exist as command executables
func MustLookupPaths(paths ...string) {
	for _, path := range paths {
		_, err := exec.LookPath(path)
		if err != nil {
			log.Panicln("Could not find path:", path)
		}
	}

}

// Exit Panicly if path does not exist
func MustPathExist(paths ...string) {
	for _, path := range paths {
		if !PathExists(path) {
			log.Panicln("Cannot continue")
		}
	}
}

// Exit Panicly if path exists
func MustPathNotExist(paths ...string) {
	for _, path := range paths {
		if PathExists(path) {
			log.Panicln("Cannot continue")
		}
	}
}

// Path does not exist
func PathExists(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Debugln("Path does not exist:", path)
		return false
	}
	log.Debugln("Path Exists:", path)
	return true
}

// Path exists
func PathNotExist(path string) bool {
	if !PathExists(path) {
		return true
	}
	return false
}

func PathIsDir(path string) bool {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return st.IsDir()
}

func PathIsSocket(path string) bool {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}

	return st.Mode()&os.ModeSocket != 0
}

// Check for successful execution and return stdout and stderr as strings
func CheckExecWithOutput(command string, commandLine ...string) (string, string, error) {
	log.Debugln("Executing Command:", command, commandLine)
	cmd := exec.Command(command, commandLine...)

	stdoutBuffer := new(bytes.Buffer)
	stderrBuffer := new(bytes.Buffer)

	cmd.Stdout = io.MultiWriter(stdoutBuffer,
		NewLogWriter(log.With("pipe", "stdout").With("cmd", command).Debugln))
	cmd.Stderr = io.MultiWriter(stderrBuffer,
		NewLogWriter(log.With("pipe", "stderr").With("cmd", command).Debugln))

	if err := cmd.Start(); err != nil {
		return "", "", err
	}

	// Wait on a go-routine for the process to exit
	doneCh := make(chan error)
	go func() {
		doneCh <- cmd.Wait()
	}()

	// Wait for process exit or global interrupt
	select {
	case err := <-doneCh:
		if err != nil {
			return "", "", err
		}
	case <-interruptCh:
		cmd.Process.Kill()
		log.Panicln("Interrupted by external request.")
	}

	return stdoutBuffer.String(), stderrBuffer.String(), nil
}

func CheckExecWithEnv(env []string, command string, commandLine ...string) error {
	log.Debugln("Executing Command:", command, commandLine)
	cmd := exec.Command(command, commandLine...)

	cmd.Env = env
	cmd.Stdout = NewLogWriter(log.With("pipe", "stdout").With("cmd", command).Debugln)
	cmd.Stderr = NewLogWriter(log.With("pipe", "stderr").With("cmd", command).Debugln)

	if err := cmd.Start(); err != nil {
		return err
	}

	// Wait on a go-routine for the process to exit
	doneCh := make(chan error)
	go func() {
		doneCh <- cmd.Wait()
	}()

	// Wait for process exit or global interrupt
	select {
	case err := <-doneCh:
		if err != nil {
			return err
		}
	case <-interruptCh:
		cmd.Process.Kill()
		log.Panicln("Interrupted by external request.")
	}

	return nil
}

// Returns a command object which logs its stdout/stderr
func LoggedCommand(command string, commandLine ...string) *exec.Cmd {
	log.Debugln("Executing Command:", command, commandLine)
	cmd := exec.Command(command, commandLine...)

	cmd.Stdout = NewLogWriter(log.With("pipe", "stdout").With("cmd", command).Debugln)
	cmd.Stderr = NewLogWriter(log.With("pipe", "stderr").With("cmd", command).Debugln)

	return cmd
}

// Checks for successful execution. Logs all output at default level.
func CheckExec(command string, commandLine ...string) error {
	log.Debugln("Executing Command:", command, commandLine)
	cmd := exec.Command(command, commandLine...)

	cmd.Stdout = NewLogWriter(log.With("pipe", "stdout").With("cmd", command).Debugln)
	cmd.Stderr = NewLogWriter(log.With("pipe", "stderr").With("cmd", command).Debugln)

	if err := cmd.Start(); err != nil {
		return err
	}

	// Wait on a go-routine for the process to exit
	doneCh := make(chan error)
	go func() {
		doneCh <- cmd.Wait()
	}()

	// Wait for process exit or global interrupt
	select {
	case err := <-doneCh:
		if err != nil {
			return err
		}
	case <-interruptCh:
		cmd.Process.Kill()
		log.Panicln("Interrupted by external request.")
	}

	return nil
}

func MustExecWithOutput(command string, commandLine ...string) (string, string) {
	stdout, stderr, err := CheckExecWithOutput(command, commandLine...)
	if err != nil {
		log.Panicln("Cannot continue - command failed:", command, commandLine, err)
	}
	return stdout, stderr
}

func MustExecWithEnv(env []string, command string, commandLine ...string) {
	err := CheckExecWithEnv(env, command, commandLine...)
	if err != nil {
		log.Panicln("Cannot continue - command failed:", command, commandLine, err)
	}
}

// Exit program if execution is not successful
func MustExec(command string, commandLine ...string) {
	err := CheckExec(command, commandLine...)
	if err != nil {
		log.Panicln("Cannot continue - command failed:", command, commandLine, err)
	}
}

// Get the current executable's folder or fail
func MustExecutableFolder() string {
	folder, err := osext.ExecutableFolder()
	if err != nil {
		log.Panicln("MustExecutableFolder", err)
	}
	return folder
}

func GetFilePerms(filename string) (os.FileMode, error) {
	st, err := os.Stat(filename)
	if err != nil {
		return os.FileMode(0777), err
	}
	return st.Mode(), nil
}

func MustGetFileSize(filename string) int64 {
	size, err := GetFileSize(filename)
	if err != nil {
		log.Panicln("MustGetFileSize:", err)
	}
	return size
}

func GetFileSize(filename string) (int64, error) {
	st, err := os.Stat(filename)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
