package main

type ErrCode string

func main() {
	panic(ErrCode("bad \"thing\""))
}
