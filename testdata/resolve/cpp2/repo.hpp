#pragma once
#include <string>
namespace store {
class Repo {
public:
  void save(int x);
  static Repo create();
  int count() { return 1; }
};
}
class Logger { public: void log(); };
