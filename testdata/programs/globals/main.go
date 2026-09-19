package main

var (
	a = b + 1
	b = f()
	c int
	d = "global"
)

var counter int

type Config struct {
	Name  string
	Level int
}

var cfg = Config{Name: "default", Level: 1}

func f() int {
	counter++
	return 41
}

func init() {
	c = a * 2
	println("init 1", a, b, c)
}

func init() {
	cfg.Level++
	println("init 2", cfg.Name, cfg.Level)
}

func bump() int {
	counter += 10
	return counter
}

func main() {
	println(a, b, c, d, counter)
	println(bump(), bump(), counter)
	p := &cfg
	p.Name = "changed"
	println(cfg.Name)
}
