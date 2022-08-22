package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"reflect"
	"strings"
)

type Star string
type Out struct{ io.Writer }
type Err struct{ io.Writer }
type In struct{ io.Reader }

type Shell struct {
	Wd    string
	Error error
}

var defaultShell *Shell

func init() {
	defaultShell = NewShell()
}

func NewShell() *Shell {
	wd, _ := os.Getwd()
	return &Shell{
		Wd: wd,
	}
}

func (sh *Shell) Exec(args ...any) *exec.Cmd {
	sargs := make([]string, 0, len(args))
	var stdout io.Writer = os.Stdout
	var stderr io.Writer = os.Stderr
	var stdin io.Reader = os.Stdin
	for _, arg := range args {
		switch arg := arg.(type) {
		case Star:
			dir, err := os.ReadDir(path.Join(sh.Wd, string(arg)))
			if err != nil {
				panic(err)
			}
			for _, e := range dir {
				sargs = append(sargs, path.Join(sh.Wd, string(arg), e.Name()))
			}
		case In:
			stdin = arg
		case Out:
			stdout = arg
		case Err:
			stderr = arg

		default:
			v := reflect.ValueOf(arg)
			if v.Kind() == reflect.String {
				sargs = append(sargs, strings.Split(v.String(), " ")...)
			} else {
				sargs = append(sargs, fmt.Sprint(arg))
			}
		}
	}
	cmd := exec.Command(sargs[0], sargs[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd
}

func (sh *Shell) Output(args ...any) ([]byte, error) {
	buf := &bytes.Buffer{}
	args = append(args, Out{buf})
	cmd := sh.Exec(args...)
	err := cmd.Run()
	return buf.Bytes(), err
}

func (sh *Shell) ChainRun(args ...any) {
	if sh.Error == nil {
		sh.Error = sh.Exec(args...).Run()
	}
}

func (sh *Shell) MustChainRun(args ...any) {
	err := sh.Exec(args...).Run()
	if err != nil {
		panic(err)
	}
}

func Redirect(cmd *exec.Cmd) *exec.Cmd {
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
