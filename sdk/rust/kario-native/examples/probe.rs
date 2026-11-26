//! probe.c (jsrt/testdata/native) in Rust, built as a cdylib: the same
//! exports, so jsrt/native_test.go drives it with the same main.js, plus
//! what the crate adds (typed arguments, errors, panics).
//!
//!     cargo build --release --example probe   # target/release/examples/libprobe.so

use kario_native::{Call, Error, Json, Kind, Module};
use std::sync::OnceLock;
use std::thread::{self, ThreadId};
use std::time::Duration;

static MAIN: OnceLock<ThreadId> = OnceLock::new();

fn add(c: &mut Call) -> Result<f64, Error> {
    Ok(c.arg::<f64>(0)? + c.arg::<f64>(1)?)
}

fn upper(c: &mut Call) -> Result<String, Error> {
    Ok(c.arg::<&str>(0)?.to_ascii_uppercase())
}

fn invert(c: &mut Call) -> Result<usize, Error> {
    let b = c
        .bytes_mut(0)
        .ok_or_else(|| Error::new("invert: the argument is bytes"))?;
    b.iter_mut().for_each(|x| *x = 255 - *x);
    Ok(b.len())
}

fn reversed(c: &mut Call) -> Result<(), Error> {
    let input: &[u8] = c.arg(0)?;
    c.ret_bytes_with(input.len(), |out| {
        out.iter_mut()
            .zip(input.iter().rev())
            .for_each(|(o, i)| *o = *i)
    })
}

fn echo(c: &mut Call) -> Result<Option<Json>, Error> {
    Ok(c.json(0).map(|j| Json(j.to_owned())))
}

fn types(c: &mut Call) -> Result<String, Error> {
    Ok((0..c.len())
        .map(|i| match c.kind(i) {
            Kind::Undefined => 'u',
            Kind::Null => '0',
            Kind::Bool => 'b',
            Kind::Number => 'n',
            Kind::String => 's',
            Kind::Bytes => 'y',
            Kind::Object => 'o',
        })
        .collect())
}

fn fail(c: &mut Call) -> Result<f64, Error> {
    Err(Error::new(c.string(0).unwrap_or("failed")))
}

fn bad_json(_: &mut Call) -> Result<Json, Error> {
    Ok(Json("{oops".into()))
}

fn slow(c: &mut Call) -> Result<Json, Error> {
    thread::sleep(Duration::from_millis(c.arg::<u64>(0)?));
    Ok(Json(format!(
        "{{\"worker\":{}}}",
        Some(&thread::current().id()) != MAIN.get()
    )))
}

fn crash(_: &mut Call) -> Result<(), Error> {
    unsafe { std::ptr::null_mut::<i32>().write_volatile(1) };
    Ok(())
}

fn typed(c: &mut Call) -> Result<i32, Error> {
    Ok(c.arg::<i32>(0)? * 2)
}

fn thrown(_: &mut Call) -> Result<(), Error> {
    panic!("thrown: from Rust")
}

fn unknown(_: &mut Call) -> Result<i32, Error> {
    Ok("x".parse::<i32>()?)
}

kario_native::module!(|m: &mut Module| {
    let _ = MAIN.set(thread::current().id());
    let events = m.emitter();
    m.function("add", add)?;
    m.function("upper", upper)?;
    m.function("invert", invert)?;
    m.function("reversed", reversed)?;
    m.function_async("reversedAsync", reversed)?;
    m.function("echo", echo)?;
    m.function_async("echoAsync", echo)?;
    m.function("types", types)?;
    m.function("fail", fail)?;
    m.function_async("failAsync", fail)?;
    m.function("badJson", bad_json)?;
    m.function_async("slow", slow)?;
    m.function("start", move |c: &mut Call| {
        let n = c.arg::<u32>(0)?;
        thread::spawn(move || {
            for i in 0..n {
                events.emit("tick", Some(&format!("{{\"i\":{i}}}")));
            }
            events.emit("done", None);
        });
        Ok(())
    })?;
    m.function("crash", crash)?;
    m.function_async("crashAsync", crash)?;
    m.function("typed", typed)?;
    m.function("thrown", thrown)?;
    m.function_async("thrownAsync", thrown)?;
    m.function("unknown", unknown)?;
    let engine = m.engine().to_owned();
    m.string("engine", &engine)?;
    let (major, minor) = m.version();
    m.number("abi", f64::from(major) * 100.0 + f64::from(minor))?;
    if m.function("on", add).is_ok() {
        return Err(Error::new("exported the runtime's own \"on\""));
    }
    Ok(())
});
