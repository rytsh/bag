namespace App.Data {
  public interface IRepo { void Save(string x); }
  public class Repo : IRepo {
    public void Save(string x) {}
    public static Repo Create() { return new Repo(); }
  }
  public class Cache { }
}
