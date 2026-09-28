#include "repo.hpp"
class Service {
public:
  store::Repo* repo;
  void helper();
  void run() {
    store::Repo r;
    r.save(1);
    r.count();
    Logger* lg = new Logger();
    lg->log();
    Logger& ref = *lg;
    ref.log();
    Repo::create();
    Logger::log();
    this->helper();
    Unknown u;
    u.go();
    Missing::thing();
    std::string s;
    s.size();
    int a, b;
    Logger x1, x2;
    x1.log();
  }
};
void Service::helper() {}
