#include "repo.hpp"
class Service {
public:
  Repo* repo;
  void run() {
    Repo r;
    r.save(1);
    repo->save(2);
    Repo::create();
  }
};
