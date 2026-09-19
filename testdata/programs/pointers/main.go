package main

type Node struct {
	Val  int
	Next *Node
}

func swap(a, b *int) { *a, *b = *b, *a }

func main() {
	x, y := 1, 2
	swap(&x, &y)
	println(x, y)

	p := &x
	pp := &p
	**pp = 42
	println(x, p == &x, p != &y)

	type pair struct{ a, b int }
	v := pair{1, 2}
	fa := &v.a
	*fa = 7
	w := v
	*fa = 8
	println(v.a, w.a)

	var arr [4]int
	e := &arr[2]
	*e = 5
	arr[2]++
	println(arr[0], arr[2], *e)

	var head *Node
	for i := 1; i <= 5; i++ {
		head = &Node{Val: i, Next: head}
	}
	sum := 0
	for n := head; n != nil; n = n.Next {
		sum += n.Val
		print(n.Val, " ")
	}
	println()
	println("sum", sum, head.Next.Next.Val)

	q := new(int)
	*q += 3
	println(*q, head != nil)
}
