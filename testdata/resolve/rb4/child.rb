module Billing
  class Child < Base
    def run
      compute
      process
      helper
    end

    def helper; end
  end

  Invoice = Struct.new(:a) do
    def total; a; end
  end

  Err = Class.new(StandardError)
end

class ::Top
  def go; end
end

class Billing::Child
  def extra; end
end
class Leaf < Billing::Child
  def leaf
    helper
    compute
    b = Billing::Child.new
    b.compute
  end
end
