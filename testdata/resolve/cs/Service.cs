using App.Data;
namespace App {
  public class Cache { }
  public class Service {
    private readonly IRepo _repo;
    public Service(IRepo repo) { _repo = repo; }
    public void Run(Repo r) {
      _repo.Save("a");
      r.Save("b");
      Repo.Create();
      var c = new App.Data.Cache();
    }
  }
}
