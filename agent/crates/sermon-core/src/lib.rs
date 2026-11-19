//! Domain types and ports (`Collector`, `LogSource`, `EventParser`, `Clock`) shared by all agent crates.

pub mod clock;
pub mod proc_root;
#[cfg(any(test, feature = "testing"))]
pub mod testing;

pub use clock::{Clock, Monotonic, SystemClock};
pub use proc_root::{ProcError, ProcRoot};
