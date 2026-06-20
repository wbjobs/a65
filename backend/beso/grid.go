package beso

import "math"

type Voxel struct {
	X, Y, Z        int
	Exists         bool
	Stress         float64
	Sensitivity    float64
	IsLoad         bool
	IsSupport      bool
	FatigueDamage  float64
	StressHistory  []float64
	MaxStress      float64
	MinStress      float64
}

type DynamicLoad struct {
	X, Y, Z        int
	BaseMagnitude  float64
	Amplitude      float64
	Frequency      float64
	DirectionX     float64
	DirectionY     float64
	DirectionZ     float64
	IsAreaLoad     bool
	AreaRadius     int
}

type Grid struct {
	SizeX, SizeY, SizeZ int
	Voxels              []Voxel
	LoadPoints          []int
	SupportPoints       []int
	DynamicLoads        []DynamicLoad
	TotalVolume         int
	CurrentVolume       int
	TargetVolumeRatio   float64
	Iteration           int
	TimeStep            int
	Time                float64
	FatigueHistorySize  int
	UseDynamicLoad      bool
	DynamicFrequency    float64
	DynamicAmplitude    float64
	StepsPerAnalysis    int
}

const (
	DEFAULT_FATIGUE_HISTORY = 50
	FATIGUE_LIMIT           = 0.7
	FATIGUE_WARNING         = 0.4
)

func NewGrid(sizeX, sizeY, sizeZ int) *Grid {
	g := &Grid{
		SizeX:             sizeX,
		SizeY:             sizeY,
		SizeZ:             sizeZ,
		TargetVolumeRatio: 0.4,
		FatigueHistorySize: DEFAULT_FATIGUE_HISTORY,
		StepsPerAnalysis:  10,
		DynamicFrequency:  1.0,
		DynamicAmplitude:  0.5,
	}
	total := sizeX * sizeY * sizeZ
	g.Voxels = make([]Voxel, total)
	g.TotalVolume = total
	g.CurrentVolume = total
	for i := 0; i < total; i++ {
		x, y, z := g.IndexToCoord(i)
		g.Voxels[i] = Voxel{
			X:             x,
			Y:             y,
			Z:             z,
			Exists:        true,
			Stress:        0,
			StressHistory: make([]float64, 0, DEFAULT_FATIGUE_HISTORY),
			MaxStress:     0,
			MinStress:     0,
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
		g.AddDynamicLoad(x, y, z, false, 0)
	}
}

func (g *Grid) AddDynamicLoad(x, y, z int, isArea bool, radius int) {
	dl := DynamicLoad{
		X:             x,
		Y:             y,
		Z:             z,
		BaseMagnitude: 1.0,
		Amplitude:     g.DynamicAmplitude,
		Frequency:     g.DynamicFrequency,
		DirectionX:    0,
		DirectionY:    -1,
		DirectionZ:    0,
		IsAreaLoad:    isArea,
		AreaRadius:    radius,
	}
	g.DynamicLoads = append(g.DynamicLoads, dl)
}

func (g *Grid) SetDynamicParams(frequency, amplitude float64) {
	g.DynamicFrequency = frequency
	g.DynamicAmplitude = amplitude
	for i := range g.DynamicLoads {
		g.DynamicLoads[i].Frequency = frequency
		g.DynamicLoads[i].Amplitude = amplitude
	}
}

func (g *Grid) GetCurrentLoadMagnitude(dl *DynamicLoad) float64 {
	return dl.BaseMagnitude + dl.Amplitude*math.Sin(2*math.Pi*dl.Frequency*g.Time)
}

func (g *Grid) GetAffectedLoadPoints(dl *DynamicLoad) []int {
	points := make([]int, 0)
	if !dl.IsAreaLoad {
		idx := g.Index(dl.X, dl.Y, dl.Z)
		if g.InBounds(dl.X, dl.Y, dl.Z) {
			points = append(points, idx)
		}
		return points
	}
	for dx := -dl.AreaRadius; dx <= dl.AreaRadius; dx++ {
		for dy := -dl.AreaRadius; dy <= dl.AreaRadius; dy++ {
			for dz := -dl.AreaRadius; dz <= dl.AreaRadius; dz++ {
				nx, ny, nz := dl.X+dx, dl.Y+dy, dl.Z+dz
				if g.InBounds(nx, ny, nz) {
					dist := math.Sqrt(float64(dx*dx + dy*dy + dz*dz))
					if dist <= float64(dl.AreaRadius) {
						points = append(points, g.Index(nx, ny, nz))
					}
				}
			}
		}
	}
	return points
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
	for i, dl := range g.DynamicLoads {
		if dl.X == x && dl.Y == y && dl.Z == z {
			g.DynamicLoads = append(g.DynamicLoads[:i], g.DynamicLoads[i+1:]...)
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

func (g *Grid) RecordStressForFatigue() {
	for i := range g.Voxels {
		v := &g.Voxels[i]
		if !v.Exists && !v.IsLoad && !v.IsSupport {
			continue
		}
		v.StressHistory = append(v.StressHistory, v.Stress)
		if len(v.StressHistory) > g.FatigueHistorySize {
			v.StressHistory = v.StressHistory[1:]
		}
		if v.Stress > v.MaxStress {
			v.MaxStress = v.Stress
		}
		if v.Stress < v.MinStress {
			v.MinStress = v.Stress
		}
	}
}

func (g *Grid) ComputeFatigueDamage() {
	for i := range g.Voxels {
		v := &g.Voxels[i]
		if !v.Exists && !v.IsLoad && !v.IsSupport {
			v.FatigueDamage = 0
			continue
		}
		if len(v.StressHistory) < 2 {
			v.FatigueDamage = 0
			continue
		}
		stressRange := v.MaxStress - v.MinStress
		if stressRange < 0.01 {
			v.FatigueDamage = 0
			continue
		}
		meanStress := (v.MaxStress + v.MinStress) / 2.0
		goodmanCorrection := stressRange / (1.0 - meanStress)
		if goodmanCorrection <= 0 {
			goodmanCorrection = stressRange
		}
		cycles := float64(len(v.StressHistory)) / 2.0
		if goodmanCorrection > 0.1 {
			v.FatigueDamage = cycles / math.Pow(1.0/goodmanCorrection, 3.0)
		} else {
			v.FatigueDamage = 0
		}
		if v.FatigueDamage > 1.0 {
			v.FatigueDamage = 1.0
		}
	}
}

func (g *Grid) StepTime() {
	g.TimeStep++
	g.Time = float64(g.TimeStep) * 0.01
}

func (g *Grid) ShouldDoAnalysis() bool {
	return g.TimeStep%g.StepsPerAnalysis == 0
}

func (g *Grid) Reset() {
	for i := range g.Voxels {
		g.Voxels[i].Exists = true
		g.Voxels[i].Stress = 0
		g.Voxels[i].Sensitivity = 0
		g.Voxels[i].IsLoad = false
		g.Voxels[i].IsSupport = false
		g.Voxels[i].FatigueDamage = 0
		g.Voxels[i].StressHistory = g.Voxels[i].StressHistory[:0]
		g.Voxels[i].MaxStress = 0
		g.Voxels[i].MinStress = 0
	}
	g.LoadPoints = g.LoadPoints[:0]
	g.SupportPoints = g.SupportPoints[:0]
	g.DynamicLoads = g.DynamicLoads[:0]
	g.CurrentVolume = g.TotalVolume
	g.Iteration = 0
	g.TimeStep = 0
	g.Time = 0
	g.UseDynamicLoad = false
}
