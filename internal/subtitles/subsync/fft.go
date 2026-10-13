package subsync

import (
	"math"
	"math/bits"
	"math/cmplx"
)

// fftPlan transforms complex vectors of one power-of-two length.
type fftPlan struct {
	n       int
	shift   int
	twiddle []complex128 // e^(-2πik/n) for k < n/2
}

func newFFTPlan(n int) *fftPlan {
	p := &fftPlan{n: n, shift: 64 - bits.TrailingZeros(uint(n)), twiddle: make([]complex128, n/2)}
	for k := range p.twiddle {
		p.twiddle[k] = cmplx.Rect(1, -2*math.Pi*float64(k)/float64(n))
	}
	return p
}

// transform runs the FFT of a in place, or the unscaled inverse.
func (p *fftPlan) transform(a []complex128, inverse bool) {
	n := p.n
	for i := range n {
		j := int(bits.Reverse64(uint64(i)) >> p.shift)
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half, stride := size/2, n/size
		for start := 0; start < n; start += size {
			for k := range half {
				w := p.twiddle[k*stride]
				if inverse {
					w = cmplx.Conj(w)
				}
				even, odd := a[start+k], a[start+k+half]*w
				a[start+k] = even + odd
				a[start+k+half] = even - odd
			}
		}
	}
}

// correlator computes c[k] = sum_i x[i]*y[i+k] for one x against many y,
// all within its plan's length, reusing its buffers.
type correlator struct {
	plan *fftPlan
	fx   []complex128 // conjugated FFT of x
	nx   int
	work []complex128
}

func newCorrelator(plan *fftPlan, x []float64) *correlator {
	c := &correlator{plan: plan, fx: make([]complex128, plan.n), nx: len(x)}
	for i, v := range x {
		c.fx[i] = complex(v, 0)
	}
	plan.transform(c.fx, false)
	for i := range c.fx {
		c.fx[i] = cmplx.Conj(c.fx[i])
	}
	return c
}

// correlate returns c[k] for k in [0, len(y)-len(x)]; out is reused when it
// is long enough. len(y) must not exceed the plan's length.
func (c *correlator) correlate(y []float64, out []float64) []float64 {
	lags := len(y) - c.nx + 1
	if c.nx == 0 || lags <= 0 || len(y) > c.plan.n {
		return nil
	}
	if cap(c.work) < c.plan.n {
		c.work = make([]complex128, c.plan.n)
	}
	work := c.work[:c.plan.n]
	clear(work)
	for i, v := range y {
		work[i] = complex(v, 0)
	}
	c.plan.transform(work, false)
	for i := range work {
		work[i] *= c.fx[i]
	}
	c.plan.transform(work, true)
	if cap(out) < lags {
		out = make([]float64, lags)
	}
	out = out[:lags]
	scale := 1 / float64(c.plan.n)
	for k := range out {
		out[k] = real(work[k]) * scale
	}
	return out
}

// crossCorrelate is the direct form, for short searches.
func crossCorrelate(x, y []float64) []float64 {
	lags := len(y) - len(x) + 1
	if len(x) == 0 || lags <= 0 {
		return nil
	}
	out := make([]float64, lags)
	for k := range out {
		sum := 0.0
		for i, v := range x {
			sum += v * y[i+k]
		}
		out[k] = sum
	}
	return out
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
