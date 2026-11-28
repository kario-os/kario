//! Native modules of Kario's TypeScript apps in Rust: the crate over the C
//! interface `kario_native.h` (sdk/c/include, 22-developer-api.md §9.3),
//! which stays what the runtime speaks. A module is a `cdylib`; it loads in
//! kario-jsrt on the phone and in Node.js on the developer machine alike,
//! and links nothing of either.
//!
//! ```ignore
//! use kario_native::{Call, Error, Module};
//!
//! fn add(c: &mut Call) -> Result<f64, Error> {
//!     Ok(c.arg::<f64>(0)? + c.arg::<f64>(1)?)
//! }
//!
//! fn invert(c: &mut Call) -> Result<(), Error> {
//!     let px = c.bytes_mut(0).ok_or_else(|| Error::new("invert: the argument is bytes"))?;
//!     px.iter_mut().for_each(|b| *b = 255 - *b);
//!     Ok(())
//! }
//!
//! kario_native::module!(|m: &mut Module| {
//!     m.function("add", add)?;
//!     m.function_async("invertAsync", invert)?;
//!     m.string("version", "1.2")
//! });
//! ```
//!
//! with `crate-type = ["cdylib"]` in the module's Cargo.toml; the library
//! (`lib<name>.so`) goes into the project as `native/<arch>/<name>.so`.
//!
//! A function returns `Result<R, Error>` where R becomes the JavaScript
//! result (`()`, a number, `bool`, `String`, `&str`, `Vec<u8>`, [`Json`],
//! an `Option` of one); an `Err` makes the call throw an `Error` with its
//! message, and so does a panic, which never unwinds into the runtime.
//! What the accessors borrow lives until the function returns. A function
//! exported with `function_async` runs on a worker thread, so it is `Send +
//! Sync`; threads of the module's own report through an [`Emitter`].

use std::cell::Cell;
use std::ffi::{c_char, c_int, c_void, CStr, CString};
use std::fmt;
use std::panic::{catch_unwind, AssertUnwindSafe};

/// The interface version the crate is built for (`KARIO_NATIVE_ABI_*`).
pub const ABI_MAJOR: u16 = 1;
pub const ABI_MINOR: u16 = 0;
pub const ABI: u32 = (ABI_MAJOR as u32) << 16 | ABI_MINOR as u32;

/// The C interface as it is: `kario_native.h`'s types and table.
pub mod sys {
    use super::*;

    #[repr(C)]
    pub struct Module {
        _private: [u8; 0],
    }

    #[repr(C)]
    pub struct Call {
        _private: [u8; 0],
    }

    pub const UNDEFINED: c_int = 0;
    pub const NULL: c_int = 1;
    pub const BOOL: c_int = 2;
    pub const NUMBER: c_int = 3;
    pub const STRING: c_int = 4;
    pub const BYTES: c_int = 5;
    pub const OBJECT: c_int = 6;

    /// `export_fn`'s flags.
    pub const ASYNC: u32 = 1;

    pub type Fn = unsafe extern "C" fn(api: *const Api, call: *mut Call, data: *mut c_void);

    /// `struct kario_api`, member for member.
    #[repr(C)]
    pub struct Api {
        pub size: u32,
        pub major: u16,
        pub minor: u16,
        pub engine: *const c_char,
        pub export_fn:
            unsafe extern "C" fn(*mut Module, *const c_char, Fn, *mut c_void, u32) -> c_int,
        pub export_number: unsafe extern "C" fn(*mut Module, *const c_char, f64) -> c_int,
        pub export_string: unsafe extern "C" fn(*mut Module, *const c_char, *const c_char) -> c_int,
        pub argc: unsafe extern "C" fn(*mut Call) -> c_int,
        pub type_: unsafe extern "C" fn(*mut Call, c_int) -> c_int,
        pub number: unsafe extern "C" fn(*mut Call, c_int) -> f64,
        pub boolean: unsafe extern "C" fn(*mut Call, c_int) -> c_int,
        pub string: unsafe extern "C" fn(*mut Call, c_int, *mut usize) -> *const c_char,
        pub bytes: unsafe extern "C" fn(*mut Call, c_int, *mut usize) -> *mut c_void,
        pub json: unsafe extern "C" fn(*mut Call, c_int, *mut usize) -> *const c_char,
        pub ret_number: unsafe extern "C" fn(*mut Call, f64),
        pub ret_bool: unsafe extern "C" fn(*mut Call, c_int),
        pub ret_string: unsafe extern "C" fn(*mut Call, *const c_char, usize),
        pub ret_bytes: unsafe extern "C" fn(*mut Call, usize) -> *mut c_void,
        pub ret_json: unsafe extern "C" fn(*mut Call, *const c_char, usize),
        pub fail: unsafe extern "C" fn(*mut Call, *const c_char),
        pub emit: unsafe extern "C" fn(*mut Module, *const c_char, *const c_char, usize) -> c_int,
    }
}

/// What makes a call throw: its message becomes the `Error`'s.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error(String);

impl Error {
    pub fn new(message: impl Into<String>) -> Error {
        Error(message.into())
    }

    pub fn message(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl From<&str> for Error {
    fn from(s: &str) -> Error {
        Error(s.to_owned())
    }
}

impl From<String> for Error {
    fn from(s: String) -> Error {
        Error(s)
    }
}

macro_rules! from_std_error {
    ($($t:ty),*) => {$(
        impl From<$t> for Error {
            fn from(e: $t) -> Error {
                Error(e.to_string())
            }
        }
    )*};
}

from_std_error!(
    std::io::Error,
    std::fmt::Error,
    std::str::Utf8Error,
    std::string::FromUtf8Error,
    std::num::ParseIntError,
    std::num::ParseFloatError
);

/// An argument's JavaScript type.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Undefined,
    Null,
    Bool,
    Number,
    String,
    /// An ArrayBuffer, or a typed array's bytes.
    Bytes,
    /// Anything else (objects, arrays): read it as JSON.
    Object,
}

impl Kind {
    fn of(t: c_int) -> Kind {
        match t {
            sys::NULL => Kind::Null,
            sys::BOOL => Kind::Bool,
            sys::NUMBER => Kind::Number,
            sys::STRING => Kind::String,
            sys::BYTES => Kind::Bytes,
            sys::OBJECT => Kind::Object,
            _ => Kind::Undefined,
        }
    }

    fn describe(self) -> &'static str {
        match self {
            Kind::Undefined => "undefined",
            Kind::Null => "null",
            Kind::Bool => "a boolean",
            Kind::Number => "a number",
            Kind::String => "a string",
            Kind::Bytes => "bytes",
            Kind::Object => "an object",
        }
    }
}

/// JSON text: an argument read as JSON, or a result JavaScript parses.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Json(pub String);

/// One call: its arguments and its result. Arguments count from 0.
pub struct Call {
    api: &'static sys::Api,
    c: *mut sys::Call,
    name: &'static str,
    filling: Cell<bool>,
}

impl Call {
    /// The exported name, for messages.
    pub fn name(&self) -> &str {
        self.name
    }

    pub fn len(&self) -> usize {
        unsafe { (self.api.argc)(self.c) as usize }
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    pub fn kind(&self, i: usize) -> Kind {
        Kind::of(unsafe { (self.api.type_)(self.c, index(i)) })
    }

    /// The argument is undefined or null (or missing).
    pub fn is_missing(&self, i: usize) -> bool {
        matches!(self.kind(i), Kind::Undefined | Kind::Null)
    }

    /// JavaScript's truthiness, of any argument.
    pub fn truthy(&self, i: usize) -> bool {
        unsafe { (self.api.boolean)(self.c, index(i)) != 0 }
    }

    pub fn number(&self, i: usize) -> Option<f64> {
        (self.kind(i) == Kind::Number).then(|| unsafe { (self.api.number)(self.c, index(i)) })
    }

    /// A string argument; None unless a string, or for one that is not
    /// UTF-8 (QuickJS gives a lone surrogate as such bytes).
    pub fn string(&self, i: usize) -> Option<&str> {
        std::str::from_utf8(self.string_bytes(i)?).ok()
    }

    /// A string argument's UTF-8 bytes as the runtime gives them.
    pub fn string_bytes(&self, i: usize) -> Option<&[u8]> {
        let mut n = 0usize;
        let p = unsafe { (self.api.string)(self.c, index(i), &mut n) };
        (!p.is_null()).then(|| unsafe { std::slice::from_raw_parts(p as *const u8, n) })
    }

    /// Bytes: JavaScript's own memory in a plain function, a copy in an
    /// asynchronous one.
    pub fn bytes(&self, i: usize) -> Option<&[u8]> {
        let mut n = 0usize;
        let p = unsafe { (self.api.bytes)(self.c, index(i), &mut n) };
        (!p.is_null()).then(|| unsafe { std::slice::from_raw_parts(p as *const u8, n) })
    }

    /// The same bytes to change in place: in a plain function the caller
    /// sees the change.
    pub fn bytes_mut(&mut self, i: usize) -> Option<&mut [u8]> {
        let mut n = 0usize;
        let p = unsafe { (self.api.bytes)(self.c, index(i), &mut n) };
        (!p.is_null()).then(|| unsafe { std::slice::from_raw_parts_mut(p as *mut u8, n) })
    }

    /// Any argument as JSON text; None when JSON cannot hold it.
    pub fn json(&self, i: usize) -> Option<&str> {
        let mut n = 0usize;
        let p = unsafe { (self.api.json)(self.c, index(i), &mut n) };
        (!p.is_null()).then(|| unsafe {
            std::str::from_utf8_unchecked(std::slice::from_raw_parts(p as *const u8, n))
        })
    }

    /// Argument i as T, or an error saying what it is instead (see
    /// [`FromArg`]).
    pub fn arg<'a, T: FromArg<'a>>(&'a self, i: usize) -> Result<T, Error> {
        T::from_arg(self, i)
    }

    /// The result is an ArrayBuffer of len bytes, zeroed, that fill writes
    /// without a copy; return `()` from the function after it.
    pub fn ret_bytes_with<F: FnOnce(&mut [u8])>(&self, len: usize, fill: F) -> Result<(), Error> {
        if self.filling.replace(true) {
            return Err(Error::new("ret_bytes_with inside ret_bytes_with"));
        }
        let p = unsafe { (self.api.ret_bytes)(self.c, len) };
        let out = if p.is_null() {
            Err(Error::new(format!("{}: out of memory", self.name)))
        } else {
            fill(unsafe { std::slice::from_raw_parts_mut(p as *mut u8, len) });
            Ok(())
        };
        self.filling.set(false);
        out
    }

    fn wrong(&self, i: usize, what: &str) -> Error {
        Error(format!("{}: argument {} {}", self.name, i + 1, what))
    }

    fn expect(&self, i: usize, kind: Kind, what: &str) -> Result<(), Error> {
        match self.kind(i) {
            k if k == kind => Ok(()),
            k => Err(self.wrong(i, &format!("is {}, not {}", k.describe(), what))),
        }
    }
}

fn index(i: usize) -> c_int {
    c_int::try_from(i).unwrap_or(c_int::MAX)
}

/// The types arguments come as: f64 and f32, the integer types (an
/// integral number in range), bool, &str and String, &[u8] and Vec<u8>,
/// [`Json`] (any value JSON can hold), and an `Option` of one (None for
/// undefined or null).
pub trait FromArg<'a>: Sized {
    fn from_arg(call: &'a Call, i: usize) -> Result<Self, Error>;
}

impl<'a> FromArg<'a> for f64 {
    fn from_arg(c: &'a Call, i: usize) -> Result<f64, Error> {
        c.expect(i, Kind::Number, "a number")?;
        Ok(c.number(i).unwrap_or(f64::NAN))
    }
}

impl<'a> FromArg<'a> for f32 {
    fn from_arg(c: &'a Call, i: usize) -> Result<f32, Error> {
        Ok(f64::from_arg(c, i)? as f32)
    }
}

macro_rules! int_arg {
    ($($t:ty),*) => {$(
        impl<'a> FromArg<'a> for $t {
            fn from_arg(c: &'a Call, i: usize) -> Result<$t, Error> {
                let v = f64::from_arg(c, i)?;
                if v.fract() != 0.0 || v < <$t>::MIN as f64 || v > <$t>::MAX as f64 {
                    return Err(c.wrong(i, "is not an integer in range"));
                }
                Ok(v as $t)
            }
        }
    )*};
}

int_arg!(i8, i16, i32, i64, isize, u8, u16, u32, u64, usize);

impl<'a> FromArg<'a> for bool {
    fn from_arg(c: &'a Call, i: usize) -> Result<bool, Error> {
        c.expect(i, Kind::Bool, "a boolean")?;
        Ok(c.truthy(i))
    }
}

impl<'a> FromArg<'a> for &'a str {
    fn from_arg(c: &'a Call, i: usize) -> Result<&'a str, Error> {
        c.expect(i, Kind::String, "a string")?;
        c.string(i)
            .ok_or_else(|| c.wrong(i, "is not UTF-8 (a lone surrogate)"))
    }
}

impl<'a> FromArg<'a> for String {
    fn from_arg(c: &'a Call, i: usize) -> Result<String, Error> {
        <&str>::from_arg(c, i).map(str::to_owned)
    }
}

impl<'a> FromArg<'a> for &'a [u8] {
    fn from_arg(c: &'a Call, i: usize) -> Result<&'a [u8], Error> {
        c.expect(i, Kind::Bytes, "bytes")?;
        Ok(c.bytes(i).unwrap_or(&[]))
    }
}

impl<'a> FromArg<'a> for Vec<u8> {
    fn from_arg(c: &'a Call, i: usize) -> Result<Vec<u8>, Error> {
        <&[u8]>::from_arg(c, i).map(<[u8]>::to_vec)
    }
}

impl<'a> FromArg<'a> for Json {
    fn from_arg(c: &'a Call, i: usize) -> Result<Json, Error> {
        match c.json(i) {
            Some(j) => Ok(Json(j.to_owned())),
            None => Err(c.wrong(
                i,
                &format!("is {}, which JSON cannot hold", c.kind(i).describe()),
            )),
        }
    }
}

impl<'a, T: FromArg<'a>> FromArg<'a> for Option<T> {
    fn from_arg(c: &'a Call, i: usize) -> Result<Option<T>, Error> {
        if c.is_missing(i) {
            return Ok(None);
        }
        T::from_arg(c, i).map(Some)
    }
}

/// What a function's value becomes in JavaScript.
pub trait IntoResult {
    fn set(self, call: &Call);
}

impl IntoResult for () {
    fn set(self, _: &Call) {}
}

macro_rules! number_result {
    ($($t:ty),*) => {$(
        impl IntoResult for $t {
            fn set(self, c: &Call) {
                unsafe { (c.api.ret_number)(c.c, self as f64) }
            }
        }
    )*};
}

number_result!(f64, f32, i8, i16, i32, i64, isize, u8, u16, u32, u64, usize);

impl IntoResult for bool {
    fn set(self, c: &Call) {
        unsafe { (c.api.ret_bool)(c.c, self as c_int) }
    }
}

impl IntoResult for &str {
    fn set(self, c: &Call) {
        unsafe { (c.api.ret_string)(c.c, self.as_ptr() as *const c_char, self.len()) }
    }
}

impl IntoResult for String {
    fn set(self, c: &Call) {
        self.as_str().set(c)
    }
}

impl IntoResult for &[u8] {
    fn set(self, c: &Call) {
        let _ = c.ret_bytes_with(self.len(), |out| out.copy_from_slice(self));
    }
}

impl IntoResult for Vec<u8> {
    fn set(self, c: &Call) {
        self.as_slice().set(c)
    }
}

impl IntoResult for Json {
    fn set(self, c: &Call) {
        unsafe { (c.api.ret_json)(c.c, self.0.as_ptr() as *const c_char, self.0.len()) }
    }
}

impl<T: IntoResult> IntoResult for Option<T> {
    fn set(self, c: &Call) {
        if let Some(v) = self {
            v.set(c)
        }
    }
}

type Exported = dyn Fn(&mut Call) -> Result<(), Error> + Send + Sync;

struct Export {
    f: Box<Exported>,
    name: String,
}

fn panic_message(p: &(dyn std::any::Any + Send)) -> &str {
    p.downcast_ref::<&str>()
        .copied()
        .or_else(|| p.downcast_ref::<String>().map(String::as_str))
        .unwrap_or("a panic")
}

unsafe extern "C" fn trampoline(api: *const sys::Api, c: *mut sys::Call, data: *mut c_void) {
    let e: &'static Export = &*(data as *const Export);
    let api: &'static sys::Api = &*api;
    let mut call = Call {
        api,
        c,
        name: &e.name,
        filling: Cell::new(false),
    };
    let message = match catch_unwind(AssertUnwindSafe(|| (e.f)(&mut call))) {
        Ok(Ok(())) => return,
        Ok(Err(err)) => err.0,
        Err(p) => format!("{} panicked: {}", e.name, panic_message(&*p)),
    };
    let message = CString::new(message.replace('\0', " ")).unwrap_or_default();
    (api.fail)(c, message.as_ptr());
}

/// The module being loaded, inside [`module!`]'s function.
pub struct Module {
    api: &'static sys::Api,
    m: *mut sys::Module,
}

impl Module {
    /// "QuickJS-ng 0.18.0", "Node.js 24.14.0": for a log line.
    pub fn engine(&self) -> &str {
        unsafe { CStr::from_ptr(self.api.engine) }
            .to_str()
            .unwrap_or("")
    }

    /// The runtime's interface version (major, minor).
    pub fn version(&self) -> (u16, u16) {
        (self.api.major, self.api.minor)
    }

    /// Exports f as name, called on the engine's thread.
    pub fn function<F, R>(&mut self, name: &str, f: F) -> Result<(), Error>
    where
        F: Fn(&mut Call) -> Result<R, Error> + Send + Sync + 'static,
        R: IntoResult,
    {
        self.export(name, f, 0)
    }

    /// Exports f as name, called on a worker thread with copies of its
    /// arguments; JavaScript gets a Promise.
    pub fn function_async<F, R>(&mut self, name: &str, f: F) -> Result<(), Error>
    where
        F: Fn(&mut Call) -> Result<R, Error> + Send + Sync + 'static,
        R: IntoResult,
    {
        self.export(name, f, sys::ASYNC)
    }

    fn export<F, R>(&mut self, name: &str, f: F, flags: u32) -> Result<(), Error>
    where
        F: Fn(&mut Call) -> Result<R, Error> + Send + Sync + 'static,
        R: IntoResult,
    {
        let cname = self.name(name)?;
        let wrapped = move |c: &mut Call| -> Result<(), Error> {
            let v = f(c)?;
            v.set(c);
            Ok(())
        };
        // Modules are never unloaded: the export lives as long as the
        // process.
        let data = Box::into_raw(Box::new(Export {
            f: Box::new(wrapped),
            name: name.to_owned(),
        }));
        if unsafe {
            (self.api.export_fn)(
                self.m,
                cname.as_ptr(),
                trampoline,
                data as *mut c_void,
                flags,
            )
        } != 0
        {
            drop(unsafe { Box::from_raw(data) });
            return Err(refused(name));
        }
        Ok(())
    }

    pub fn number(&mut self, name: &str, v: f64) -> Result<(), Error> {
        let cname = self.name(name)?;
        match unsafe { (self.api.export_number)(self.m, cname.as_ptr(), v) } {
            0 => Ok(()),
            _ => Err(refused(name)),
        }
    }

    pub fn string(&mut self, name: &str, v: &str) -> Result<(), Error> {
        let cname = self.name(name)?;
        let cv =
            CString::new(v).map_err(|_| Error::new(format!("\"{name}\": a string with a NUL")))?;
        match unsafe { (self.api.export_string)(self.m, cname.as_ptr(), cv.as_ptr()) } {
            0 => Ok(()),
            _ => Err(refused(name)),
        }
    }

    /// What the module's own threads report through.
    pub fn emitter(&self) -> Emitter {
        Emitter {
            api: self.api,
            m: self.m,
        }
    }

    fn name(&self, name: &str) -> Result<CString, Error> {
        CString::new(name).map_err(|_| refused(name))
    }
}

fn refused(name: &str) -> Error {
    Error::new(format!("cannot export \"{name}\" (a bad or repeated name)"))
}

/// Events from any thread to the handlers JavaScript gave the module's
/// `on(event, handler)`, in order, on the engine's thread.
#[derive(Clone, Copy)]
pub struct Emitter {
    api: &'static sys::Api,
    m: *mut sys::Module,
}

// emit is the one function of the table any thread may call, and the
// module it names lives as long as the process.
unsafe impl Send for Emitter {}
unsafe impl Sync for Emitter {}

impl Emitter {
    /// false when the queue is full (JavaScript is not keeping up) or json
    /// is not JSON; None: the handler gets undefined.
    pub fn emit(&self, event: &str, json: Option<&str>) -> bool {
        let Ok(event) = CString::new(event) else {
            return false;
        };
        let (p, n) = json.map_or((std::ptr::null(), 0), |j| {
            (j.as_ptr() as *const c_char, j.len())
        });
        unsafe { (self.api.emit)(self.m, event.as_ptr(), p, n) == 0 }
    }
}

#[doc(hidden)]
pub unsafe fn __init<F>(api: *const sys::Api, m: *mut sys::Module, init: F) -> c_int
where
    F: FnOnce(&mut Module) -> Result<(), Error>,
{
    let mut module = Module { api: &*api, m };
    match catch_unwind(AssertUnwindSafe(|| init(&mut module))) {
        Ok(Ok(())) => 0,
        Ok(Err(e)) => {
            eprintln!("native module: {e}");
            1
        }
        Err(p) => {
            eprintln!("native module: init panicked: {}", panic_message(&*p));
            1
        }
    }
}

/// `module!(init)` is `KARIO_NATIVE_MODULE` for Rust: it defines what the
/// runtime looks up in the library, and init (a function or closure taking
/// `&mut Module`) exports the names. An `Err` or a panic from it refuses
/// the module, its message in the log.
#[macro_export]
macro_rules! module {
    ($init:expr) => {
        #[no_mangle]
        #[allow(non_upper_case_globals)]
        pub static kario_native_abi: u32 = $crate::ABI;

        /// # Safety
        /// The runtime calls it once, with its table and the module.
        #[no_mangle]
        pub unsafe extern "C" fn kario_native_init(
            api: *const $crate::sys::Api,
            m: *mut $crate::sys::Module,
        ) -> ::std::ffi::c_int {
            $crate::__init(api, m, $init)
        }
    };
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abi() {
        assert_eq!(ABI, 0x10000);
        // The header's members: 8 bytes of version, the engine and 17 functions.
        assert_eq!(
            std::mem::size_of::<sys::Api>(),
            8 + 18 * std::mem::size_of::<usize>()
        );
    }
}
