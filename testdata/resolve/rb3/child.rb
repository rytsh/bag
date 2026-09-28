module Billing
  class Child < Base
    def run
      compute()
      process()
      build
    end
    def self.make
      build
    end
  end
end
class Leaf < Billing::Child
  def leaf
    compute()
    run()
    b = Billing::Child.new
    b.compute
    c = Leaf.new
    c.process
  end
end
