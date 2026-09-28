require_relative 'repo'
class Service
  def run
    r = Repo.new
    r.save(1)
    Repo.create
  end
end
