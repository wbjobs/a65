package beso

import (
	"math"
	"sort"
)

type VoxelChange struct {
	Index   int
	X, Y, Z int
	Action  byte
	Stress  float64
}

const (
	ActionAdd    = 1
	ActionRemove = 2
	ActionKeep   = 0
)

func (g *Grid) getNeighbors(idx int) []int {
	x, y, z := g.IndexToCoord(idx)
	neighbors := make([]int, 0, 6)
	dirs := [][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}, {0, 0, 1}, {0, 0, -1}}
	for _, d := range dirs {
		nx, ny, nz := x+d[0], y+d[1], z+d[2]
		if g.InBounds(nx, ny, nz) {
			neighbors = append(neighbors, g.Index(nx, ny, nz))
		}
	}
	return neighbors
}

func (g *Grid) getDiagonalNeighbors(idx int) []int {
	x, y, z := g.IndexToCoord(idx)
	neighbors := make([]int, 0, 26)
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for dz := -1; dz <= 1; dz++ {
				if dx == 0 && dy == 0 && dz == 0 {
					continue
				}
				nx, ny, nz := x+dx, y+dy, z+dz
				if g.InBounds(nx, ny, nz) {
					neighbors = append(neighbors, g.Index(nx, ny, nz))
				}
			}
		}
	}
	return neighbors
}

func (g *Grid) ComputeStress() {
	for i := range g.Voxels {
		g.Voxels[i].Stress = 0
	}

	if len(g.LoadPoints) == 0 || len(g.SupportPoints) == 0 {
		return
	}

	for _, loadIdx := range g.LoadPoints {
		if !g.Voxels[loadIdx].Exists {
			continue
		}
		lx, ly, lz := g.IndexToCoord(loadIdx)

		for _, supportIdx := range g.SupportPoints {
			if !g.Voxels[supportIdx].Exists {
				continue
			}
			sx, sy, sz := g.IndexToCoord(supportIdx)

			dx := float64(sx - lx)
			dy := float64(sy - ly)
			dz := float64(sz - lz)
			dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
			if dist < 1 {
				dist = 1
			}

			steps := int(dist) + 2
			for s := 0; s <= steps; s++ {
				t := float64(s) / float64(steps)
				px := int(math.Round(float64(lx) + dx*t))
				py := int(math.Round(float64(ly) + dy*t))
				pz := int(math.Round(float64(lz) + dz*t))

				if g.InBounds(px, py, pz) {
					idx := g.Index(px, py, pz)
					if g.Voxels[idx].Exists {
						perpendicularSpread := 1
						for ox := -perpendicularSpread; ox <= perpendicularSpread; ox++ {
							for oy := -perpendicularSpread; oy <= perpendicularSpread; oy++ {
								for oz := -perpendicularSpread; oz <= perpendicularSpread; oz++ {
									nx, ny, nz := px+ox, py+oy, pz+oz
									if g.InBounds(nx, ny, nz) {
										nidx := g.Index(nx, ny, nz)
										if g.Voxels[nidx].Exists {
											offDist := math.Sqrt(float64(ox*ox + oy*oy + oz*oz))
											weight := 1.0 / (1.0 + offDist*2.0)
											g.Voxels[nidx].Stress += weight / dist
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	maxStress := 0.0
	for i := range g.Voxels {
		if g.Voxels[i].Exists && g.Voxels[i].Stress > maxStress {
			maxStress = g.Voxels[i].Stress
		}
	}
	if maxStress > 0 {
		for i := range g.Voxels {
			g.Voxels[i].Stress /= maxStress
		}
	}
}

func (g *Grid) ComputeSensitivity(history []*Grid) {
	for i := range g.Voxels {
		v := &g.Voxels[i]
		if !v.Exists {
			neighbors := g.getDiagonalNeighbors(i)
			avgNeighborStress := 0.0
			existingCount := 0
			for _, ni := range neighbors {
				if g.Voxels[ni].Exists {
					avgNeighborStress += g.Voxels[ni].Stress
					existingCount++
				}
			}
			if existingCount > 0 {
				v.Sensitivity = avgNeighborStress / float64(existingCount) * 0.8
			} else {
				v.Sensitivity = 0
			}
		} else {
			v.Sensitivity = v.Stress
		}

		if v.IsLoad || v.IsSupport {
			v.Sensitivity = 1000.0
		}
	}

	if len(history) >= 2 {
		prev := history[len(history)-1]
		prev2 := history[len(history)-2]
		for i := range g.Voxels {
			g.Voxels[i].Sensitivity = (g.Voxels[i].Sensitivity +
				prev.Voxels[i].Sensitivity +
				prev2.Voxels[i].Sensitivity) / 3.0
		}
	}
}

func (g *Grid) Evolve(er, ar float64) []VoxelChange {
	g.ComputeStress()

	history := []*Grid{}
	g.ComputeSensitivity(history)

	existingSensitivities := make([]struct {
		idx         int
		sensitivity float64
	}, 0, g.CurrentVolume)
	missingSensitivities := make([]struct {
		idx         int
		sensitivity float64
	}, 0, g.TotalVolume-g.CurrentVolume)

	for i := range g.Voxels {
		if g.Voxels[i].IsLoad || g.Voxels[i].IsSupport {
			continue
		}
		if g.Voxels[i].Exists {
			existingSensitivities = append(existingSensitivities, struct {
				idx         int
				sensitivity float64
			}{i, g.Voxels[i].Sensitivity})
		} else {
			missingSensitivities = append(missingSensitivities, struct {
				idx         int
				sensitivity float64
			}{i, g.Voxels[i].Sensitivity})
		}
	}

	sort.Slice(existingSensitivities, func(i, j int) bool {
		return existingSensitivities[i].sensitivity < existingSensitivities[j].sensitivity
	})
	sort.Slice(missingSensitivities, func(i, j int) bool {
		return missingSensitivities[i].sensitivity > missingSensitivities[j].sensitivity
	})

	changes := make([]VoxelChange, 0)

	targetVol := int(float64(g.TotalVolume) * g.TargetVolumeRatio)
	currentRatio := float64(g.CurrentVolume) / float64(g.TotalVolume)

	var removeCount, addCount int
	if currentRatio > g.TargetVolumeRatio {
		removeCount = int(float64(len(existingSensitivities)) * er)
		addCount = int(float64(len(missingSensitivities)) * ar * 0.5)
	} else {
		removeCount = int(float64(len(existingSensitivities)) * er * 0.3)
		addCount = int(float64(len(missingSensitivities)) * ar)
	}

	if g.CurrentVolume-removeCount+addCount < targetVol {
		removeCount = g.CurrentVolume - targetVol + addCount
		if removeCount < 0 {
			removeCount = 0
		}
	}
	if removeCount > len(existingSensitivities) {
		removeCount = len(existingSensitivities)
	}
	if addCount > len(missingSensitivities) {
		addCount = len(missingSensitivities)
	}

	removed := 0
	for i := 0; i < removeCount && removed < removeCount; i++ {
		idx := existingSensitivities[i].idx
		if g.Voxels[idx].Exists && !g.Voxels[idx].IsLoad && !g.Voxels[idx].IsSupport {
			g.Voxels[idx].Exists = false
			x, y, z := g.IndexToCoord(idx)
			changes = append(changes, VoxelChange{
				Index:  idx,
				X:      x,
				Y:      y,
				Z:      z,
				Action: ActionRemove,
				Stress: g.Voxels[idx].Stress,
			})
			g.CurrentVolume--
			removed++
		}
	}

	added := 0
	for i := 0; i < addCount && added < addCount; i++ {
		idx := missingSensitivities[i].idx
		if !g.Voxels[idx].Exists {
			neighbors := g.getNeighbors(idx)
			hasExistingNeighbor := false
			for _, ni := range neighbors {
				if g.Voxels[ni].Exists {
					hasExistingNeighbor = true
					break
				}
			}
			if g.Voxels[idx].IsLoad || g.Voxels[idx].IsSupport || hasExistingNeighbor {
				g.Voxels[idx].Exists = true
				x, y, z := g.IndexToCoord(idx)
				changes = append(changes, VoxelChange{
					Index:  idx,
					X:      x,
					Y:      y,
					Z:      z,
					Action: ActionAdd,
					Stress: g.Voxels[idx].Stress,
				})
				g.CurrentVolume++
				added++
			}
		}
	}

	g.Iteration++
	return changes
}

func (g *Grid) IsConverged() bool {
	currentRatio := float64(g.CurrentVolume) / float64(g.TotalVolume)
	return math.Abs(currentRatio-g.TargetVolumeRatio) < 0.01 || g.Iteration >= 100
}

func (g *Grid) GetVolumeRatio() float64 {
	return float64(g.CurrentVolume) / float64(g.TotalVolume)
}
