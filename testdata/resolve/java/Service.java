package app;
public class Service {
  private Repo repo;
  public Service(Repo repo) { this.repo = repo; }
  public void run(Repo other) {
    repo.save("a");
    this.repo.save("b");
    other.save("c");
    Repo.create();
    Repo local = new Repo();
    local.save("d");
  }
}
