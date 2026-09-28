module Billing
  class Base
    def process
      compute
    end

    def compute
      1
    end

    def self.build
      new
    end

    class << self
      def factory
        build
      end
    end
  end
end
