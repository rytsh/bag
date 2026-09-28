package com.x;
public class P {
  private Gateway gw;
  void go(Client c) { gw.send(); c.call(); External.run(); this.gw.ping(); }
}
