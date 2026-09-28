using Infra.Data;
using Oc = Other.Cache;
namespace App {
  public class Svc : BaseSvc {
    private Cache _c;
    public Oc Alt { get; set; }
    public void Run(Cache p) {
      _c.Get();
      Alt.Get();
      base.Ping();
      this.Run(p);
      var x = new Other.Cache();
      x.Get();
      if (p is Cache q) { q.Get(); }
    }
  }
}
