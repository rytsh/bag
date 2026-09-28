import models
from models import Repo
import models as m

class Service:
    def run(self):
        Repo.create()
        models.helper()
        m.helper()
        r = Repo()
        r.save(1)
