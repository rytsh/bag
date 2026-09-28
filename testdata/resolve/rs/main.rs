mod repo;
use repo::Repo;
fn main() { let r = Repo{}; r.save(2); Repo::helper(); }
