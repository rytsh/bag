class Service {
  let repo: Repo
  init(repo: Repo) { self.repo = repo }
  func run() {
    repo.save(1)
    Repo.make()
    let r = Repo()
    r.save(2)
    let f = Repo.make()
    f.save(3)
  }
}
