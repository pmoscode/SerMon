//! Domain types and ports (`Collector`, `LogSource`, `EventParser`, `Clock`) shared by all agent crates.

pub mod clock;
pub mod host;
pub mod metrics;
pub mod proc_root;
#[cfg(any(test, feature = "testing"))]
pub mod testing;

pub use clock::{Clock, Monotonic, SystemClock};
pub use host::{Architecture, HostFacts, MachineId, SecuritySource};
pub use metrics::{PPM, Sample, Value};
pub use proc_root::{ProcError, ProcRoot};
