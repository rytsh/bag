pub struct Repo {}
impl Repo {
    pub fn save(&self, x: i32) {}
    pub fn run(&self) { self.save(1); Self::helper(); }
    fn helper() {}
}
