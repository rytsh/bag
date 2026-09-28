package com.x;
import java.util.List;
public class Service extends Base {
  private Repo own;
  public void run(Repo p, Helper h) {
    own.flush();
    p.flush();
    this.repo.flush();
    this.own.flush();
    Repo.create();
    Repo local = new Repo();
    local.flush();
    h.help();
    this.helper();
    remote.call();
    List<String> xs = null;
    xs.add("a");
    local.save(1);
    Runnable r = (Repo q) -> q.flush();
  }
  private void helper() {}
}
class Helper { void help() {} }
