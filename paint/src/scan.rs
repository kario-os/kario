//! The spans tiny-skia's anti-aliased fill finds on each quarter-pixel row
//! of a path, for raster.rs to cover rounded rectangles and circles on the
//! GPU exactly as tiny-skia does: the same fixed-point edges, the curves
//! flattened into the same lines and stepped row by row the same way, a
//! few integer operations a row. Only for paths tiny-skia does not clip
//! (inside the picture), whose curves need no chopping (monotonic in y)
//! and whose rows hold one span each; others get None.
//!
//! Ported from tiny-skia 0.11 (edge.rs, edge_builder.rs, scan/path.rs,
//! scan/path_aa.rs), itself a port of Skia, under this licence:
//!
//! Copyright (c) 2011 Google Inc. All rights reserved.
//! Copyright (c) 2020 Yevhenii Reizner All rights reserved.
//!
//! Redistribution and use in source and binary forms, with or without
//! modification, are permitted provided that the following conditions are
//! met:
//!
//!   * Redistributions of source code must retain the above copyright
//!     notice, this list of conditions and the following disclaimer.
//!
//!   * Redistributions in binary form must reproduce the above copyright
//!     notice, this list of conditions and the following disclaimer in
//!     the documentation and/or other materials provided with the
//!     distribution.
//!
//!   * Neither the name of the copyright holder nor the names of its
//!     contributors may be used to endorse or promote products derived
//!     from this software without specific prior written permission.
//!
//! THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
//! "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
//! LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
//! A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
//! OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
//! SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
//! LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
//! DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
//! THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
//! (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
//! OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

use tiny_skia::{Path, PathSegment, Point};
use tiny_skia_path::path_geometry::{find_unit_quad_roots, new_t_values};

/// Four rows and columns of samples a pixel.
const SHIFT: i32 = 2;
const MAX_COEFF_SHIFT: i32 = 6;

/// A path's spans: on quarter-pixel row top + i, the quarter-pixel columns
/// rows[i].0..rows[i].1 (empty where they are equal).
pub struct Spans {
    pub top: i32,
    pub rows: Vec<(i32, i32)>,
}

fn left_shift(v: i32, s: i32) -> i32 {
    ((v as u32) << s) as i32
}

fn round6(n: i32) -> i32 {
    (n + 32) >> 6
}

fn to16(n: i32) -> i32 {
    left_shift(n, 10)
}

fn div6(a: i32, b: i32) -> i32 {
    if i16::try_from(a).is_ok() {
        left_shift(a, 16) / b
    } else {
        ((((a as i64) as u64) << 16) as i64 / b as i64).clamp(i32::MIN as i64, i32::MAX as i64) as i32
    }
}

fn mul16(a: i32, b: i32) -> i32 {
    ((a as i64 * b as i64) >> 16) as i32
}

fn compute_dy(top: i32, y0: i32) -> i32 {
    left_shift(top, 6) + 32 - y0
}

fn cheap_distance(dx: i32, dy: i32) -> i32 {
    let (dx, dy) = (dx.abs(), dy.abs());
    if dx > dy {
        dx + (dy >> 1)
    } else {
        dy + (dx >> 1)
    }
}

fn diff_to_shift(dx: i32, dy: i32, shift_aa: i32) -> i32 {
    let dist = (cheap_distance(dx, dy) + (1 << 4)) >> (3 + shift_aa);
    (32 - dist.leading_zeros() as i32) >> 1
}

fn cubic_delta_from_line(a: i32, b: i32, c: i32, d: i32) -> i32 {
    let one_third = ((a * 8 - b * 15 + 6 * c + d) * 19) >> 9;
    let two_third = ((a + 6 * b - c * 15 + d * 8) * 19) >> 9;
    one_third.abs().max(two_third.abs())
}

/// A line of an edge on rows first..=last: x (16.16) on the first, and
/// what it moves a row.
#[derive(Clone, Copy, Debug)]
struct Run {
    first: i32,
    last: i32,
    x: i32,
    dx: i32,
}

/// The line from (x0, y0) to (x1, y1) in 26.6, y0 <= y1 (LineEdge::new).
fn run6(x0: i32, y0: i32, x1: i32, y1: i32) -> Option<Run> {
    let (top, bottom) = (round6(y0), round6(y1));
    if top == bottom {
        return None;
    }
    let slope = div6(x1 - x0, y1 - y0);
    let dy = compute_dy(top, y0);
    Some(Run { first: top, last: bottom - 1, x: to16(x0 + mul16(slope, dy)), dx: slope })
}

/// The same from 16.16 (LineEdge::update).
fn run16(x0: i32, y0: i32, x1: i32, y1: i32) -> Option<Run> {
    run6(x0 >> 10, y0 >> 10, x1 >> 10, y1 >> 10)
}

fn fixed(p: Point) -> (i32, i32) {
    let scale = (1 << (SHIFT + 6)) as f32;
    ((p.x * scale) as i32, (p.y * scale) as i32)
}

/// An edge: its lines, and +1 going down, -1 going up.
type Edge = (Vec<Run>, i32);

fn line(p: [Point; 2]) -> Option<Edge> {
    let ((mut x0, mut y0), (mut x1, mut y1)) = (fixed(p[0]), fixed(p[1]));
    let mut winding = 1;
    if y0 > y1 {
        (x0, y0, x1, y1, winding) = (x1, y1, x0, y0, -1);
    }
    Some((vec![run6(x0, y0, x1, y1)?], winding))
}

/// QuadraticEdge: the quad's lines, stepped by forward differences.
fn quad(p: [Point; 3]) -> Option<Edge> {
    let ((mut x0, mut y0), (x1, y1), (mut x2, mut y2)) = (fixed(p[0]), fixed(p[1]), fixed(p[2]));
    let mut winding = 1;
    if y0 > y2 {
        (x0, y0, x2, y2, winding) = (x2, y2, x0, y0, -1);
    }
    if round6(y0) == round6(y2) {
        return None;
    }
    let mut shift = diff_to_shift((left_shift(x1, 1) - x0 - x2) >> 2, (left_shift(y1, 1) - y0 - y2) >> 2, SHIFT);
    shift = if shift == 0 { 1 } else { shift.min(MAX_COEFF_SHIFT) };
    let mut count = (1 << shift) as i8;
    let cshift = shift - 1;
    let half = |v: i32| left_shift(v, 16 - 6 - 1);
    let (a, b) = (half(x0 - x1 - x1 + x2), to16(x1 - x0));
    let (mut qx, mut qdx, qddx) = (to16(x0), b + (a >> shift), a >> (shift - 1));
    let (a, b) = (half(y0 - y1 - y1 + y2), to16(y1 - y0));
    let (mut qy, mut qdy, qddy) = (to16(y0), b + (a >> shift), a >> (shift - 1));
    let (lx, ly) = (to16(x2), to16(y2));
    let mut runs = Vec::new();
    while count > 0 {
        loop {
            count -= 1;
            let (nx, ny) = if count > 0 {
                let n = (qx + (qdx >> cshift), qy + (qdy >> cshift));
                (qdx, qdy) = (qdx + qddx, qdy + qddy);
                n
            } else {
                (lx, ly)
            };
            let got = run16(qx, qy, nx, ny);
            (qx, qy) = (nx, ny);
            if let Some(r) = got {
                runs.push(r);
                break;
            }
            if count == 0 {
                break;
            }
        }
    }
    (!runs.is_empty()).then_some((runs, winding))
}

/// CubicEdge: the cubic's lines, stepped by forward differences.
fn cubic(p: [Point; 4]) -> Option<Edge> {
    let ((mut x0, mut y0), (mut x1, mut y1), (mut x2, mut y2), (mut x3, mut y3)) = (fixed(p[0]), fixed(p[1]), fixed(p[2]), fixed(p[3]));
    let mut winding = 1;
    if y0 > y3 {
        (x0, x3, x1, x2) = (x3, x0, x2, x1);
        (y0, y3, y1, y2) = (y3, y0, y2, y1);
        winding = -1;
    }
    if round6(y0) == round6(y3) {
        return None;
    }
    let shift = (diff_to_shift(cubic_delta_from_line(x0, x1, x2, x3), cubic_delta_from_line(y0, y1, y2, y3), 2) + 1).min(MAX_COEFF_SHIFT);
    let (mut up, mut down) = (6, shift + 6 - 10);
    if down < 0 {
        (down, up) = (0, 10 - shift);
    }
    let mut count = left_shift(-1, shift) as i8;
    let ups = |v: i32| left_shift(v, up);
    let (b, c, d) = (ups(3 * (x1 - x0)), ups(3 * (x0 - x1 - x1 + x2)), ups(x3 + 3 * (x1 - x2) - x0));
    let (mut cx, mut cdx, mut cddx, cdddx) = (to16(x0), b + (c >> shift) + (d >> (2 * shift)), 2 * c + ((3 * d) >> (shift - 1)), (3 * d) >> (shift - 1));
    let (b, c, d) = (ups(3 * (y1 - y0)), ups(3 * (y0 - y1 - y1 + y2)), ups(y3 + 3 * (y1 - y2) - y0));
    let (mut cy, mut cdy, mut cddy, cdddy) = (to16(y0), b + (c >> shift) + (d >> (2 * shift)), 2 * c + ((3 * d) >> (shift - 1)), (3 * d) >> (shift - 1));
    let (lx, ly) = (to16(x3), to16(y3));
    let mut runs = Vec::new();
    while count < 0 {
        loop {
            count += 1;
            let (nx, mut ny) = if count < 0 {
                let nx = cx + (cdx >> down);
                cdx += cddx >> shift;
                cddx += cdddx;
                let ny = cy + (cdy >> down);
                cdy += cddy >> shift;
                cddy += cdddy;
                (nx, ny)
            } else {
                (lx, ly)
            };
            ny = ny.max(cy);
            let got = run16(cx, cy, nx, ny);
            (cx, cy) = (nx, ny);
            if let Some(r) = got {
                runs.push(r);
                break;
            }
            if count == 0 {
                break;
            }
        }
    }
    (!runs.is_empty()).then_some((runs, winding))
}

/// Whether tiny-skia would chop the quad at a y extremum (is_not_monotonic
/// and a t it can divide for).
fn chops_quad(a: f32, b: f32, c: f32) -> bool {
    let (ab, bc) = (a - b, b - c);
    let bc = if ab < 0.0 { -bc } else { bc };
    (ab == 0.0 || bc < 0.0) && tiny_skia_path::path_geometry::valid_unit_divide(a - b, a - b - b + c).is_some()
}

/// Whether tiny-skia would chop the cubic at a y extremum.
fn chops_cubic(a: f32, b: f32, c: f32, d: f32) -> bool {
    let mut t = new_t_values();
    find_unit_quad_roots(d - a + 3.0 * (b - c), 2.0 * (a - b - b + c), b - a, &mut t) > 0
}

/// The spans of path filled (winding) on a picture of `size`; None where
/// tiny-skia would clip, chop or find two spans on a row.
pub fn spans(path: &Path, size: (u32, u32)) -> Option<Spans> {
    let b = path.bounds();
    let ir = (b.left().floor(), b.top().floor(), b.right().ceil(), b.bottom().ceil());
    if !(ir.0 >= 0.0 && ir.1 >= 0.0 && ir.2 <= size.0 as f32 && ir.3 <= size.1 as f32) {
        return None;
    }
    let (super_left, start, stop) = ((ir.0 as i32) << SHIFT, (ir.1 as i32) << SHIFT, (ir.3 as i32) << SHIFT);
    let mut edges = Vec::new();
    let (mut first, mut last) = (Point::zero(), Point::zero());
    for seg in path.segments() {
        let e = match seg {
            PathSegment::MoveTo(p) => {
                if first != last {
                    edges.extend(line([last, first]));
                }
                (first, last) = (p, p);
                continue;
            }
            PathSegment::LineTo(p) => line([last, p]),
            PathSegment::QuadTo(p1, p2) => {
                if chops_quad(last.y, p1.y, p2.y) {
                    return None;
                }
                quad([last, p1, p2])
            }
            PathSegment::CubicTo(p1, p2, p3) => {
                if chops_cubic(last.y, p1.y, p2.y, p3.y) {
                    return None;
                }
                cubic([last, p1, p2, p3])
            }
            PathSegment::Close => {
                let e = line([last, first]);
                last = first;
                edges.extend(e);
                continue;
            }
        };
        last = match seg {
            PathSegment::LineTo(p) | PathSegment::QuadTo(_, p) | PathSegment::CubicTo(_, _, p) => p,
            _ => last,
        };
        edges.extend(e);
    }
    if first != last {
        edges.extend(line([last, first]));
    }
    if edges.len() < 2 {
        return Some(Spans { top: start, rows: Vec::new() });
    }
    let mut at: Vec<Vec<(i32, i32)>> = vec![Vec::new(); (stop - start).max(0) as usize];
    for (runs, winding) in &edges {
        for r in runs {
            let mut x = r.x;
            for y in r.first..=r.last {
                if y >= start && y < stop {
                    at[(y - start) as usize].push((x, *winding));
                }
                x = x.wrapping_add(r.dx);
            }
        }
    }
    let mut rows = Vec::with_capacity(at.len());
    for mut row in at {
        row.sort_by_key(|e| e.0);
        let (mut w, mut left, mut span) = (0, 0, None);
        for (x, winding) in row {
            let x = (x + (1 << 15)) >> 16;
            if w == 0 {
                left = x;
            }
            w += winding;
            if w == 0 && x > left {
                if span.is_some() {
                    return None;
                }
                span = Some(if left < super_left { (super_left, super_left + x) } else { (left, x) });
            }
        }
        rows.push(span.unwrap_or((0, 0)));
    }
    Some(Spans { top: start, rows })
}

#[cfg(test)]
mod tests {
    use tiny_skia::{FillRule, Paint, Pixmap, Transform};

    use super::*;
    use crate::canvas::{circle_path, round_path};

    /// A pixel's coverage from spans, as tiny-skia's supersampler sums it.
    pub fn coverage(sp: &Spans, x: i32, y: i32) -> u8 {
        let mut sum = 0;
        for j in 0..4 {
            let i = y * 4 + j - sp.top;
            let Some(&(l, r)) = (i >= 0).then(|| sp.rows.get(i as usize)).flatten() else { continue };
            let n = (r.min(x * 4 + 4) - l.max(x * 4)).clamp(0, 4);
            sum += if n == 4 { if j == 3 { 63 } else { 64 } } else { 16 * n };
        }
        sum as u8
    }

    // Rounded rectangles and circles of all sizes at fractional places:
    // the coverage their spans give is tiny-skia's, pixel for pixel (an
    // opaque white fill's alpha is its coverage).
    #[test]
    fn spans_cover_as_tiny_skia_does() {
        let (w, h) = (64u32, 48u32);
        let mut seed = 0x2545_f491_4f6c_dd1du64;
        let mut f = |lo: f32, hi: f32| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            lo + (hi - lo) * ((seed >> 40) as f32 / (1u64 << 24) as f32)
        };
        let mut paint = Paint::default();
        paint.set_color_rgba8(255, 255, 255, 255);
        paint.anti_alias = true;
        let (mut shapes, mut skipped) = (0, 0);
        for i in 0..3000 {
            let s = [1.0, 2.0, 2.4, 1.7, 3.0][i % 5];
            let path = if i % 3 == 0 {
                circle_path(s, f(0.0, 3.0), f(0.0, 64.0 / s), f(0.0, 48.0 / s), f(0.05, 14.0))
            } else {
                round_path(s, f(0.0, 3.0), (f(-2.0, 40.0 / s), f(-2.0, 30.0 / s), f(0.1, 40.0 / s), f(0.1, 30.0 / s)), f(0.0, 16.0))
            };
            let Some(path) = path else { continue };
            let Some(sp) = spans(&path, (w, h)) else {
                skipped += 1;
                continue;
            };
            shapes += 1;
            let mut pm = Pixmap::new(w, h).unwrap();
            pm.fill_path(&path, &paint, FillRule::Winding, Transform::identity(), None);
            for y in 0..h as i32 {
                for x in 0..w as i32 {
                    let want = pm.data()[((y as u32 * w + x as u32) * 4 + 3) as usize];
                    assert_eq!(coverage(&sp, x, y), want, "shape {i} at ({x}, {y}): {:?}", path.bounds());
                }
            }
        }
        assert!(shapes > 1500 && skipped > 0, "{shapes} shapes, {skipped} not inside the picture");
    }
}
