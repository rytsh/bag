use crate::a::Engine;
impl Engine {
    fn apply_block(&self) {}
    fn helper(&self) { self.apply_block(); }
}
impl<U> Bucket<U> {
    fn grow(&self) {}
}
