//! Settings › Accessibility on what the panel shows: colour inversion,
//! colour correction for weak cones, and the magnifier, which zooms the
//! screen around the focus. With none of them on nothing here runs: the
//! panel and the snapshots get the frame as drawn, byte for byte.

use tiny_skia::Pixmap;

use crate::frame::Frame;

/// The cone a colour correction is for: weak red (protanomaly), green
/// (deuteranomaly) or blue (tritanomaly).
#[derive(Clone, Copy, PartialEq, Debug)]
pub enum Cvd {
    Protan,
    Deutan,
    Tritan,
}

/// What a frame asks of the picture.
#[derive(Clone, Copy, PartialEq, Debug)]
pub struct Look {
    pub invert: bool,
    pub correct: Option<Cvd>,
    /// How much the magnifier zooms; 0: it is off.
    pub zoom: f32,
}

/// The most the magnifier zooms: each step more draws the frame on a
/// picture that much bigger each way.
const MAX_ZOOM: f32 = 4.0;

impl Look {
    pub fn of(f: &Frame) -> Look {
        let correct = match f.daltonize.as_str() {
            "protanomaly" => Some(Cvd::Protan),
            "deuteranomaly" => Some(Cvd::Deutan),
            "tritanomaly" => Some(Cvd::Tritan),
            _ => None,
        };
        let zoom = if f.magnify > 1.0 { f.magnify.min(MAX_ZOOM).round() } else { 0.0 };
        Look { invert: f.invert, correct, zoom }
    }

    /// Nothing to do: the picture is the frame.
    pub fn plain(&self) -> bool {
        !self.invert && self.correct.is_none() && self.zoom == 0.0
    }

    /// Recolours the picture in place (premultiplied, opaque).
    pub fn filter(&self, pm: &mut Pixmap) {
        if !self.invert && self.correct.is_none() {
            return;
        }
        let mut f = Filter::new(self);
        for px in pm.data_mut().chunks_exact_mut(4) {
            let [r, g, b] = f.pixel([px[0], px[1], px[2]]);
            (px[0], px[1], px[2]) = (r, g, b);
        }
    }
}

type M3 = [[f64; 3]; 3];

const IDENTITY: M3 = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]];

fn mul(a: &M3, b: &M3) -> M3 {
    let mut out = [[0.0; 3]; 3];
    for (i, row) in out.iter_mut().enumerate() {
        for (j, v) in row.iter_mut().enumerate() {
            *v = (0..3).map(|k| a[i][k] * b[k][j]).sum();
        }
    }
    out
}

fn sub(a: &M3, b: &M3) -> M3 {
    let mut out = *a;
    for i in 0..3 {
        for j in 0..3 {
            out[i][j] -= b[i][j];
        }
    }
    out
}

fn add(a: &M3, b: &M3) -> M3 {
    let mut out = *a;
    for i in 0..3 {
        for j in 0..3 {
            out[i][j] += b[i][j];
        }
    }
    out
}

fn apply(m: &M3, v: [f64; 3]) -> [f64; 3] {
    [0, 1, 2].map(|i| m[i][0] * v[0] + m[i][1] * v[1] + m[i][2] * v[2])
}

fn inverse(m: &M3) -> M3 {
    let c = |i: usize, j: usize| m[(i + 1) % 3][(j + 1) % 3] * m[(i + 2) % 3][(j + 2) % 3] - m[(i + 1) % 3][(j + 2) % 3] * m[(i + 2) % 3][(j + 1) % 3];
    let det: f64 = (0..3).map(|j| m[0][j] * c(0, j)).sum();
    let mut out = [[0.0; 3]; 3];
    for (i, row) in out.iter_mut().enumerate() {
        for (j, v) in row.iter_mut().enumerate() {
            *v = c(j, i) / det;
        }
    }
    out
}

fn cross(a: [f64; 3], b: [f64; 3]) -> [f64; 3] {
    [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]]
}

/// Linear sRGB to the cones' responses (LMS): sRGB's XYZ, then CIECAM02's
/// CAT02.
fn rgb_to_lms() -> M3 {
    const RGB_XYZ: M3 = [[0.4124, 0.3576, 0.1805], [0.2126, 0.7152, 0.0722], [0.0193, 0.1192, 0.9505]];
    const XYZ_LMS: M3 = [[0.7328, 0.4296, -0.1624], [-0.7036, 1.6975, 0.0061], [0.0030, 0.0136, 0.9834]];
    mul(&XYZ_LMS, &RGB_XYZ)
}

/// What someone without the cone sees, in LMS: the colour moved along that
/// cone's axis onto the plane through black, white and blue (red for the
/// blue cone), whose colours they see as everyone does (Brettel, Viénot
/// and Mollon 1997, as Android's Daltonizer).
fn projection(cvd: Cvd) -> M3 {
    let lms = rgb_to_lms();
    let column = |j: usize| [lms[0][j], lms[1][j], lms[2][j]];
    let white = apply(&lms, [1.0, 1.0, 1.0]);
    let p = cross(white, column(2));
    let q = cross(white, column(0));
    match cvd {
        Cvd::Protan => [[0.0, -p[1] / p[0], -p[2] / p[0]], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]],
        Cvd::Deutan => [[1.0, 0.0, 0.0], [-p[0] / p[1], 0.0, -p[2] / p[1]], [0.0, 0.0, 1.0]],
        Cvd::Tritan => [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [-q[0] / q[2], -q[1] / q[2], 0.0]],
    }
}

/// The correction in linear sRGB: what the weak cone misses of a colour is
/// spread onto the cones that work, 0.7 of it to each (Android's
/// Daltonizer), so colours that cone alone told apart differ to the others.
pub fn correction(cvd: Cvd) -> M3 {
    let lms = rgb_to_lms();
    let s = projection(cvd);
    let spread = match cvd {
        Cvd::Protan => [[1.0, 0.0, 0.0], [0.7, 1.0, 0.0], [0.7, 0.0, 1.0]],
        Cvd::Deutan => [[1.0, 0.7, 0.0], [0.0, 1.0, 0.0], [0.0, 0.7, 1.0]],
        Cvd::Tritan => [[1.0, 0.0, 0.7], [0.0, 1.0, 0.7], [0.0, 0.0, 1.0]],
    };
    let inner = add(&s, &mul(&spread, &sub(&IDENTITY, &s)));
    mul(&inverse(&lms), &mul(&inner, &lms))
}

/// sRGB's transfer function, both ways.
fn to_linear(v: u8) -> f32 {
    let v = v as f32 / 255.0;
    if v <= 0.04045 { v / 12.92 } else { ((v + 0.055) / 1.055).powf(2.4) }
}

const LIN_STEPS: usize = 16384;

fn encode(v: f32) -> u8 {
    let v = v.clamp(0.0, 1.0);
    let e = if v <= 0.003_130_8 { v * 12.92 } else { 1.055 * v.powf(1.0 / 2.4) - 0.055 };
    (e * 255.0).round() as u8
}

/// Lightness inverted, hue and saturation kept: BT.601's luma Y goes to
/// 1 − Y, the chroma stays (Android's colour inversion); on the encoded
/// values, so a middle grey stays middle grey. Twice is the colour again.
pub fn invert(c: [f32; 3]) -> [f32; 3] {
    let y = 0.299 * c[0] + 0.587 * c[1] + 0.114 * c[2];
    c.map(|v| v + 1.0 - 2.0 * y)
}

/// One look's recolouring, with tables for the transfer function and a
/// small cache: a screen of a system's pages has few colours.
struct Filter {
    correct: Option<[[f32; 3]; 3]>,
    invert: bool,
    lin: [f32; 256],
    enc: Vec<u8>,
    cache: Vec<u32>,
}

const CACHE: usize = 4096;
const EMPTY: u32 = u32::MAX;

impl Filter {
    fn new(look: &Look) -> Filter {
        let correct = look.correct.map(|c| correction(c).map(|r| r.map(|v| v as f32)));
        let enc = (0..=LIN_STEPS).map(|i| encode(i as f32 / LIN_STEPS as f32)).collect();
        Filter { correct, invert: look.invert, lin: std::array::from_fn(|i| to_linear(i as u8)), enc, cache: vec![EMPTY; 2 * CACHE] }
    }

    fn pixel(&mut self, rgb: [u8; 3]) -> [u8; 3] {
        let key = u32::from_be_bytes([0, rgb[0], rgb[1], rgb[2]]);
        let slot = (key.wrapping_mul(2_654_435_761) >> 20) as usize % CACHE * 2;
        if self.cache[slot] == key {
            let v = self.cache[slot + 1].to_be_bytes();
            return [v[1], v[2], v[3]];
        }
        let mut out = rgb;
        if let Some(m) = &self.correct {
            let l = rgb.map(|v| self.lin[v as usize]);
            let at = |v: f32| self.enc[(v.clamp(0.0, 1.0) * LIN_STEPS as f32).round() as usize];
            out = [0, 1, 2].map(|i| at(m[i][0] * l[0] + m[i][1] * l[1] + m[i][2] * l[2]));
        }
        if self.invert {
            out = invert(out.map(|v| v as f32 / 255.0)).map(|v| (v.clamp(0.0, 1.0) * 255.0).round() as u8);
        }
        self.cache[slot] = key;
        self.cache[slot + 1] = u32::from_be_bytes([0, out[0], out[1], out[2]]);
        out
    }
}

/// Where the panel's window into a picture zoomed z times starts (zoomed
/// pixels): around the focus (a box in logical pixels at scale s) or the
/// middle without one, inside the picture. A focus wider or taller than
/// the window shows its start (where its text begins) or its top.
pub fn window(panel: (u32, u32), s: f32, z: f32, focus: Option<(f32, f32, f32, f32)>) -> (u32, u32) {
    let (w, h) = (panel.0 as f32, panel.1 as f32);
    let k = s * z;
    let start = |pos: f32, len: f32, side: f32| if len * k > side { pos * k } else { (pos + len / 2.0) * k - side / 2.0 };
    let (ox, oy) = match focus {
        Some((x, y, bw, bh)) => (start(x, bw, w), start(y, bh, h)),
        None => ((w * z - w) / 2.0, (h * z - h) / 2.0),
    };
    let at = |o: f32, side: f32| o.clamp(0.0, side * z - side).round() as u32;
    (at(ox, w), at(oy, h))
}

/// The panel's window of the zoomed picture.
pub fn crop(big: &Pixmap, at: (u32, u32), w: u32, h: u32) -> Pixmap {
    let mut out = Pixmap::new(w.max(1), h.max(1)).expect("panel size");
    let (bw, ow) = (big.width() as usize * 4, w as usize * 4);
    let (x0, y0) = (at.0.min(big.width().saturating_sub(w)) as usize * 4, at.1.min(big.height().saturating_sub(h)) as usize);
    let (src, dst) = (big.data(), out.data_mut());
    for y in 0..(h as usize).min(big.height() as usize) {
        let s = (y0 + y) * bw + x0;
        let n = ow.min(bw - x0);
        dst[y * ow..y * ow + n].copy_from_slice(&src[s..s + n]);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn frame(v: serde_json::Value) -> Frame {
        serde_json::from_value(v).unwrap()
    }

    fn close(a: [f64; 3], b: [f64; 3], eps: f64) -> bool {
        (0..3).all(|i| (a[i] - b[i]).abs() < eps)
    }

    #[test]
    fn a_plain_frame_is_left_alone() {
        assert!(Look::of(&Frame::default()).plain());
        let l = Look::of(&frame(serde_json::json!({"invert": true, "daltonize": "deuteranomaly", "magnify": 2})));
        assert_eq!(l, Look { invert: true, correct: Some(Cvd::Deutan), zoom: 2.0 });
        assert_eq!(Look::of(&frame(serde_json::json!({"magnify": 9, "daltonize": "x"}))), Look { invert: false, correct: None, zoom: MAX_ZOOM });
        let mut pm = Pixmap::new(3, 1).unwrap();
        pm.data_mut().copy_from_slice(&[10, 200, 30, 255, 0, 0, 0, 255, 255, 255, 255, 255]);
        let before = pm.data().to_vec();
        Look { invert: false, correct: None, zoom: 2.0 }.filter(&mut pm);
        assert_eq!(pm.data(), &before[..], "the magnifier alone does not recolour");
    }

    // Inversion: black and white swap, greys go to their mirror, a hue
    // stays (red is still the reddest channel), and twice is the colour
    // again.
    #[test]
    fn inversion_keeps_hues() {
        let mut pm = Pixmap::new(5, 1).unwrap();
        let px = [[0u8, 0, 0], [255, 255, 255], [64, 64, 64], [220, 40, 40], [16, 18, 24]];
        for (i, p) in px.iter().enumerate() {
            pm.data_mut()[i * 4..i * 4 + 4].copy_from_slice(&[p[0], p[1], p[2], 255]);
        }
        let look = Look { invert: true, correct: None, zoom: 0.0 };
        look.filter(&mut pm);
        let got: Vec<[u8; 3]> = pm.data().chunks(4).map(|c| [c[0], c[1], c[2]]).collect();
        assert_eq!(got[0], [255, 255, 255]);
        assert_eq!(got[1], [0, 0, 0]);
        assert_eq!(got[2], [191, 191, 191]);
        assert!(got[3][0] > got[3][1] && got[3][0] > got[3][2] && got[3][1] > 100, "red stays red, lighter: {:?}", got[3]);
        assert!(got[4].iter().all(|&v| v > 220), "a dark page turns light: {:?}", got[4]);
        for c in [[0.2f32, 0.5, 0.7], [0.9, 0.1, 0.3], [0.5, 0.5, 0.5]] {
            let back = invert(invert(c));
            assert!((0..3).all(|i| (back[i] - c[i]).abs() < 1e-5), "{c:?} → {back:?}");
        }
    }

    // A correction keeps the greys. Two colours that differ only in what
    // the weak cone sees look the same without it; corrected, they do not.
    #[test]
    fn corrections_separate_what_the_weak_cone_confuses() {
        let lms = rgb_to_lms();
        let to_rgb = inverse(&lms);
        let seen = |cvd: Cvd, c: [f64; 3]| apply(&mul(&to_rgb, &mul(&projection(cvd), &lms)), c);
        let dist = |a: [f64; 3], b: [f64; 3]| ((a[0] - b[0]).powi(2) + (a[1] - b[1]).powi(2) + (a[2] - b[2]).powi(2)).sqrt();
        for (k, cvd) in [Cvd::Protan, Cvd::Deutan, Cvd::Tritan].into_iter().enumerate() {
            let m = correction(cvd);
            for g in [0.0, 0.2, 0.5, 1.0] {
                assert!(close(apply(&m, [g, g, g]), [g, g, g], 1e-3), "{cvd:?}: grey {g} became {:?}", apply(&m, [g, g, g]));
            }
            let axis = [to_rgb[0][k], to_rgb[1][k], to_rgb[2][k]];
            let n = dist(axis, [0.0; 3]);
            let a = [0.5, 0.4, 0.3];
            let b = [0, 1, 2].map(|i| a[i] + 0.05 * axis[i] / n);
            let before = dist(seen(cvd, a), seen(cvd, b));
            let after = dist(seen(cvd, apply(&m, a)), seen(cvd, apply(&m, b)));
            assert!(before < 1e-9 && after > 0.01, "{cvd:?}: {before:.4} → {after:.4}");
        }
        let i = mul(&lms, &to_rgb);
        assert!((0..3).all(|r| close(i[r], IDENTITY[r], 1e-9)));
    }

    #[test]
    fn the_filter_matches_the_matrix() {
        let look = Look { invert: false, correct: Some(Cvd::Deutan), zoom: 0.0 };
        let m = correction(Cvd::Deutan);
        let mut f = Filter::new(&look);
        for rgb in [[200u8, 30, 40], [30, 160, 60], [12, 12, 12], [250, 250, 250]] {
            let lin = rgb.map(|v| to_linear(v) as f64);
            let want = apply(&m, lin).map(|v| encode(v as f32) as i32);
            let got = f.pixel(rgb);
            assert!((0..3).all(|i| (got[i] as i32 - want[i]).abs() <= 1), "{rgb:?}: {got:?} want {want:?}");
            assert_eq!(f.pixel(rgb), got, "from the cache");
        }
    }

    #[test]
    fn the_window_follows_the_focus() {
        let panel = (480, 640);
        assert_eq!(window(panel, 2.0, 2.0, None), (240, 320));
        // A row in the middle of the screen, wider than the window: its
        // start, centred the other way.
        assert_eq!(window(panel, 2.0, 2.0, Some((6.0, 150.0, 228.0, 30.0))), (24, (165.0 * 4.0 - 320.0) as u32));
        // A tile: centred on it.
        assert_eq!(window(panel, 2.0, 2.0, Some((90.0, 150.0, 60.0, 30.0))), (240, 340));
        // Near the top left: the window stops at the picture's edge.
        assert_eq!(window(panel, 2.0, 2.0, Some((0.0, 0.0, 20.0, 10.0))), (0, 0));
        assert_eq!(window(panel, 2.0, 3.0, Some((230.0, 310.0, 10.0, 10.0))), (960, 1280));
        // Taller than the window: its top.
        assert_eq!(window(panel, 2.0, 2.0, Some((6.0, 40.0, 228.0, 200.0))), (24, 160));
        let mut big = Pixmap::new(4, 4).unwrap();
        for (i, p) in big.data_mut().chunks_mut(4).enumerate() {
            p.copy_from_slice(&[i as u8, 0, 0, 255]);
        }
        let c = crop(&big, (2, 1), 2, 2);
        assert_eq!(c.data().chunks(4).map(|p| p[0]).collect::<Vec<_>>(), [6, 7, 10, 11]);
    }
}
