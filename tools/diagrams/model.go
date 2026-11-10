package main

type point [2]float64

type box struct{ X, Y, W, H float64 }
type node struct {
	ID, Title, Group, Package string
	Lines, Routes             []string
	Circle                    bool
	Box                       box
	Times                     []float64
}
type label struct {
	Text        string
	X, Y, Size  float64
	Bold        bool
	Owner, Kind string
}
type edge struct {
	ID, From, To, Relation string
	Points                 []point
	Times                  []float64
}
type group struct {
	ID, Title string
	Box       box
}
type legend struct {
	Text   string
	X, Y   float64
	Dashed bool
}
type diagram struct {
	Name, Title, Subtitle, Description string
	W, H, Duration                     float64
	Nodes                              []node
	Edges                              []edge
	Groups                             []group
	Labels                             []label
	Legend                             []legend
}
type placement struct {
	Width, Height float64
	Nodes         map[string]box
	Edges         map[string][]point
	Groups        map[string]box
}
