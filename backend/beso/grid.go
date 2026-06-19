package beso

type Voxel struct {
	X, Y, Z     int
	Exists      bool
	Stress      float64
	Sensitivity float64
	IsLoad      bool
	IsSupport   bool
}

type Grid struct {
	SizeX, SizeY, SizeZ int
	Voxels              []Voxel
	LoadPoints          []int
	SupportPoints       []int
	TotalVolume         int
	CurrentVolume       int
	TargetVolumeRatio   float64
	Iteration           int
}

func NewGrid(sizeX, sizeY, sizeZ int) *Grid {
	g := &Grid{
		SizeX:             sizeX,
		SizeY:             sizeY,
		SizeZ:             sizeZ,
		TargetVolumeRatio: 0.4,
	}
	total := sizeX * sizeY * sizeZ
	g.Voxels = make([]Voxel, total)
	g.TotalVolume = total
	g.CurrentVolume = total
	for i := 0; i < total; i++ {
		x, y, z := g.IndexToCoord(i)
		g.Voxels[i] = Voxel{
			X:      x,
			Y:      y,
			Z:      z,
			Exists: true,
			Stress: 0,
		}
	}
	return g
}

func (g *Grid) Index(x, y, z int) int {
	return x + y*g.SizeX + z*g.SizeX*g.SizeY
}

func (g *Grid) IndexToCoord(idx int) (int, int, int) {
	x := idx % g.SizeX
	y := (idx / g.SizeX) % g.SizeY
	z := idx / (g.SizeX * g.SizeY)
	return x, y, z
}

func (g *Grid) InBounds(x, y, z int) bool {
	return x >= 0 && x < g.SizeX && y >= 0 && y < g.SizeY && z >= 0 && z < g.SizeZ
}

func (g *Grid) SetLoad(x, y, z int) {
	if !g.InBounds(x, y, z) {
		return
	}
	idx := g.Index(x, y, z)
	if !g.Voxels[idx].IsLoad {
		g.Voxels[idx].IsLoad = true
		g.LoadPoints = append(g.LoadPoints, idx)
	}
}

func (g *Grid) SetSupport(x, y, z int) {
	if !g.InBounds(x, y, z) {
		return
	}
	idx := g.Index(x, y, z)
	if !g.Voxels[idx].IsSupport {
		g.Voxels[idx].IsSupport = true
		g.SupportPoints = append(g.SupportPoints, idx)
	}
}

func (g *Grid) RemoveLoad(x, y, z int) {
	if !g.InBounds(x, y, z) {
		return
	}
	idx := g.Index(x, y, z)
	g.Voxels[idx].IsLoad = false
	for i, lp := range g.LoadPoints {
		if lp == idx {
			g.LoadPoints = append(g.LoadPoints[:i], g.LoadPoints[i+1:]...)
			break
		}
	}
}

func (g *Grid) RemoveSupport(x, y, z int) {
	if !g.InBounds(x, y, z) {
		return
	}
	idx := g.Index(x, y, z)
	g.Voxels[idx].IsSupport = false
	for i, sp := range g.SupportPoints {
		if sp == idx {
			g.SupportPoints = append(g.SupportPoints[:i], g.SupportPoints[i+1:]...)
			break
		}
	}
}

func (g *Grid) Reset() {
	for i := range g.Voxels {
		g.Voxels[i].Exists = true
		g.Voxels[i].Stress = 0
		g.Voxels[i].Sensitivity = 0
		g.Voxels[i].IsLoad = false
		g.Voxels[i].IsSupport = false
	}
	g.LoadPoints = g.LoadPoints[:0]
	g.SupportPoints = g.SupportPoints[:0]
	g.CurrentVolume = g.TotalVolume
	g.Iteration = 0
}
