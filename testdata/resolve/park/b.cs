namespace App {
  public class Worker {
    private IClient _client;
    public void Run() {
      _client.Send();
      External.Do();
      var h = new Helper();
      h.Help();
    }
  }
  public class Helper { public void Help() {} }
}
