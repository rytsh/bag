pub struct Engine { x: i32 }
impl Engine {
    pub fn tick(&self) { self.apply_block(); self.helper(); self.missing(); }
}
pub struct Bucket<T> { v: T }
impl<T> Bucket<T> {
    fn put(&self) { self.grow(); }
}
